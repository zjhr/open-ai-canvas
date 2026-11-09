package app

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const promptGallerySampleJSONL = `{"id":"SD2_00001","category":"Entertainment","date":"2026-06-29","file_name":"seedance-2/videos/SD2_00001.mp4","i18n":{"en":{"p":"15-second cinematic Japanese drama pure love short film, warm golden sunlight","t":"Japanese Classroom Ambience","tags":["cinematic","romantic"]},"zh":{"p":"15秒电影级日剧纯爱短片，暖金色阳光","t":"空教室心跳","tags":["纯爱","日式"]}},"is_featured":true,"media":{"c":"seedance-2/covers/SD2_00001.jpg","v":"seedance-2/videos/SD2_00001.mp4"},"model_info":{"name":"seedance","version":"2.0"},"platform":"x","raw_p":"15秒电影级日剧纯爱短片","slug":"japanese-classroom","sourceLink":"https://x.com/JiahaoYang_art/status/2033119940216344616","spec":{"duration":15.1,"height":720,"ratio":1.78,"width":1280},"version":"1.0"}
{"id":"SD2_00002","category":"Nature","i18n":{"en":{"p":"Ocean waves crashing against a rocky coastline at sunset","t":"Coast Sunset","tags":["nature"]},"zh":{"p":"日落时分海浪拍打礁石海岸","t":"海岸日落","tags":["自然"]}},"is_featured":false,"media":{"c":"seedance-2/covers/SD2_00002.jpg"},"model_info":{"name":"seedance","version":"2.0"},"sourceLink":"https://x.com/example/status/1","spec":{"width":1280,"height":720}}
`

func newPromptGalleryTestService(t *testing.T) *Service {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+newID()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.PromptGallerySource{}, &model.PromptGalleryPrompt{}); err != nil {
		t.Fatal(err)
	}
	return &Service{repo: repository.New(db), promptGalleryRunning: map[string]bool{}, promptGalleryErr: map[string]string{}}
}

func TestPromptGalleryParseJSONLMapsBothLanguages(t *testing.T) {
	svc := &Service{}
	descriptor, ok := promptGallerySourceByID("hf-seedance-2")
	if !ok {
		t.Fatal("hf-seedance-2 descriptor missing")
	}
	rows, err := svc.parsePromptGalleryJSONL(descriptor, strings.NewReader(promptGallerySampleJSONL), 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}

	first := rows[0]
	if first.ID != "hf-seedance-2:SD2_00001" || first.ExternalID != "SD2_00001" {
		t.Fatalf("unexpected identity: %#v", first)
	}
	if first.SourceID != "hf-seedance-2" || first.Mode != "video" {
		t.Fatalf("unexpected source/mode: %#v", first)
	}
	if first.Title != "空教室心跳" {
		t.Fatalf("title should prefer Chinese, got %q", first.Title)
	}
	if !strings.Contains(first.Prompt, "cinematic Japanese drama") || !strings.Contains(first.PromptZh, "电影级日剧") {
		t.Fatalf("prompt languages not split: %q / %q", first.Prompt, first.PromptZh)
	}
	if first.Category != "Entertainment" || first.Model != "seedance 2.0" {
		t.Fatalf("unexpected category/model: %#v", first)
	}
	if !first.Featured {
		t.Fatal("is_featured not mapped")
	}
	if first.CoverPath != "seedance-2/covers/SD2_00001.jpg" {
		t.Fatalf("cover path = %q", first.CoverPath)
	}
	if first.Ratio != "1280x720" {
		t.Fatalf("ratio = %q", first.Ratio)
	}
	if !strings.Contains(first.Tags, "cinematic") || !strings.Contains(first.Tags, "seedance 2.0") {
		t.Fatalf("tags should include upstream tags and model: %q", first.Tags)
	}
	if first.SourceURL != "https://x.com/JiahaoYang_art/status/2033119940216344616" {
		t.Fatalf("source link lost: %q", first.SourceURL)
	}
}

func TestPromptGalleryParseJSONLHonoursLimit(t *testing.T) {
	svc := &Service{}
	descriptor, _ := promptGallerySourceByID("hf-seedance-2")
	rows, err := svc.parsePromptGalleryJSONL(descriptor, strings.NewReader(promptGallerySampleJSONL), 1, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("limit ignored: got %d rows", len(rows))
	}
}

func TestPromptGalleryCoverPathValidation(t *testing.T) {
	descriptor, _ := promptGallerySourceByID("hf-seedance-2")
	valid := []string{"seedance-2/covers/SD2_00001.jpg", "seedance-2/covers/0/SD2_04214.jpg"}
	for _, path := range valid {
		if _, err := promptGalleryCoverRelativePath(descriptor, path); err != nil {
			t.Fatalf("valid path rejected: %s (%v)", path, err)
		}
	}
	invalid := []string{"", "/etc/passwd", "../../secret", "seedance-2/covers/../../x.jpg", "other-dir/a.jpg", "seedance-2/covers/a.jpg?x=1", `seedance-2\covers\a.jpg`}
	for _, path := range invalid {
		if _, err := promptGalleryCoverRelativePath(descriptor, path); err == nil {
			t.Fatalf("invalid path accepted: %s", path)
		}
	}
}

func TestPromptGallerySearchFiltersAndFacets(t *testing.T) {
	svc := newPromptGalleryTestService(t)
	syncedAt := time.Now().UTC()
	rows := []model.PromptGalleryPrompt{
		{ID: "hf-seedance-2:1", SourceID: "hf-seedance-2", ExternalID: "1", Mode: "video", Title: "空教室心跳", Description: "纯爱 · 日式", Prompt: "cinematic Japanese classroom", PromptZh: "电影级日剧空教室", Category: "Entertainment", Model: "seedance 2.0", Tags: "cinematic 纯爱", SourceURL: "https://x.com/1", CoverPath: "seedance-2/covers/1.jpg", SyncedAt: syncedAt},
		{ID: "hf-seedance-2:2", SourceID: "hf-seedance-2", ExternalID: "2", Mode: "video", Title: "海岸日落", Description: "自然", Prompt: "Ocean waves at sunset", PromptZh: "日落海浪", Category: "Nature", Model: "seedance 2.0", Tags: "nature", SyncedAt: syncedAt},
		{ID: "hf-gpt-image-2:1", SourceID: "hf-gpt-image-2", ExternalID: "1", Mode: "image", Title: "灵魂画手重绘挑战", Description: "搞笑绘画", Prompt: "Redraw the attached image in MS Paint style", PromptZh: "用笨拙的方式重绘附图", Category: "Entertainment", Model: "gpt-image-2 1.0", Tags: "搞笑绘画", SyncedAt: syncedAt},
	}
	if err := svc.repo.UpsertPromptGalleryPrompts(rows); err != nil {
		t.Fatal(err)
	}

	// 中文正文检索
	result, err := svc.PromptGallerySearch(PromptGallerySearchRequest{Keyword: "海浪"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.Items[0].Title != "海岸日落" {
		t.Fatalf("Chinese keyword search failed: %#v", result)
	}

	// 英文正文检索
	result, err = svc.PromptGallerySearch(PromptGallerySearchRequest{Keyword: "MS Paint"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.Items[0].SourceID != "hf-gpt-image-2" {
		t.Fatalf("English keyword search failed: %#v", result)
	}

	// 按来源 + 类型过滤
	result, err = svc.PromptGallerySearch(PromptGallerySearchRequest{SourceID: "hf-seedance-2", Mode: "video"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 {
		t.Fatalf("source/mode filter failed: %#v", result)
	}

	// 分类过滤
	result, err = svc.PromptGallerySearch(PromptGallerySearchRequest{Category: "Nature"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 {
		t.Fatalf("category filter failed: %#v", result)
	}

	// 分页：limit/offset 只影响返回行数，不影响 total
	result, err = svc.PromptGallerySearch(PromptGallerySearchRequest{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 3 || len(result.Items) != 1 {
		t.Fatalf("pagination failed: total=%d items=%d", result.Total, len(result.Items))
	}

	// facet 计数
	result, err = svc.PromptGallerySearch(PromptGallerySearchRequest{})
	if err != nil {
		t.Fatal(err)
	}
	sourceCounts := map[string]int64{}
	for _, facet := range result.Sources {
		sourceCounts[facet.ID] = facet.Count
	}
	if sourceCounts["hf-seedance-2"] != 2 || sourceCounts["hf-gpt-image-2"] != 1 {
		t.Fatalf("source facets wrong: %#v", result.Sources)
	}
	if len(result.Categories) != 2 {
		t.Fatalf("category facets wrong: %#v", result.Categories)
	}
}

// facet 计数必须跟着当前筛选走，否则下拉里标的数字会大于选中后真正能筛出的条数。
// 每个维度排除自己：选着来源时来源列表仍要显示全部来源的计数。
func TestPromptGalleryFacetsFollowActiveFilters(t *testing.T) {
	svc := newPromptGalleryTestService(t)
	syncedAt := time.Now().UTC()
	rows := []model.PromptGalleryPrompt{
		{ID: "hf-seedance-2:1", SourceID: "hf-seedance-2", ExternalID: "1", Mode: "video", Title: "空教室心跳", Category: "Entertainment", Prompt: "cinematic classroom", SyncedAt: syncedAt},
		{ID: "hf-seedance-2:2", SourceID: "hf-seedance-2", ExternalID: "2", Mode: "video", Title: "海岸日落", Category: "Nature", Prompt: "ocean waves", SyncedAt: syncedAt},
		{ID: "hf-gpt-image-2:1", SourceID: "hf-gpt-image-2", ExternalID: "1", Mode: "image", Title: "灵魂画手", Category: "Entertainment", Prompt: "redraw in MS Paint", SyncedAt: syncedAt},
	}
	if err := svc.repo.UpsertPromptGalleryPrompts(rows); err != nil {
		t.Fatal(err)
	}

	facetCounts := func(result PromptGallerySearchResult) (map[string]int64, map[string]int64) {
		sources := map[string]int64{}
		categories := map[string]int64{}
		for _, facet := range result.Sources {
			sources[facet.ID] = facet.Count
		}
		for _, facet := range result.Categories {
			categories[facet.ID] = facet.Count
		}
		return sources, categories
	}

	// 只看视频：来源与分类都只剩视频行，图片来源不该继续挂在列表里。
	result, err := svc.PromptGallerySearch(PromptGallerySearchRequest{Mode: "video"})
	if err != nil {
		t.Fatal(err)
	}
	sources, categories := facetCounts(result)
	if sources["hf-seedance-2"] != 2 {
		t.Fatalf("video source facet wrong: %#v", sources)
	}
	if _, exists := sources["hf-gpt-image-2"]; exists {
		t.Fatalf("image source should not appear under a video-only filter: %#v", sources)
	}
	if categories["Entertainment"] != 1 || categories["Nature"] != 1 {
		t.Fatalf("video category facets wrong: %#v", categories)
	}

	// 选定来源：分类计数收窄到该来源，但来源维度要忽略“已选来源”，否则切不回去。
	result, err = svc.PromptGallerySearch(PromptGallerySearchRequest{SourceID: "hf-gpt-image-2"})
	if err != nil {
		t.Fatal(err)
	}
	sources, categories = facetCounts(result)
	if sources["hf-seedance-2"] != 2 || sources["hf-gpt-image-2"] != 1 {
		t.Fatalf("source facet must ignore the active source filter: %#v", sources)
	}
	if categories["Entertainment"] != 1 || len(categories) != 1 {
		t.Fatalf("category facet must follow the active source filter: %#v", categories)
	}

	// 选定分类：来源计数收窄到该分类，但分类维度要忽略“已选分类”。
	result, err = svc.PromptGallerySearch(PromptGallerySearchRequest{Category: "Entertainment"})
	if err != nil {
		t.Fatal(err)
	}
	sources, categories = facetCounts(result)
	if sources["hf-seedance-2"] != 1 || sources["hf-gpt-image-2"] != 1 {
		t.Fatalf("source facet must follow the active category filter: %#v", sources)
	}
	if categories["Entertainment"] != 2 || categories["Nature"] != 1 {
		t.Fatalf("category facet must ignore the active category filter: %#v", categories)
	}

	// 关键不变量：在同样的筛选下，下拉里标的数字 == 再加上那个条件之后能筛出的条数。
	// 计数必须和它所属的那组筛选配成一对，跨组比较没有意义（facet 本身也带着筛选上下文）。
	for _, tc := range []struct {
		name      string
		base      PromptGallerySearchRequest
		dimension string
	}{
		{name: "无筛选/来源", base: PromptGallerySearchRequest{}, dimension: "source"},
		{name: "无筛选/分类", base: PromptGallerySearchRequest{}, dimension: "category"},
		{name: "仅视频/来源", base: PromptGallerySearchRequest{Mode: "video"}, dimension: "source"},
		{name: "仅视频/分类", base: PromptGallerySearchRequest{Mode: "video"}, dimension: "category"},
	} {
		base, err := svc.PromptGallerySearch(tc.base)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		baseSources, baseCategories := facetCounts(base)
		ids := make([]string, 0, len(baseSources))
		if tc.dimension == "source" {
			for id := range baseSources {
				ids = append(ids, id)
			}
		} else {
			for id := range baseCategories {
				ids = append(ids, id)
			}
		}
		for _, id := range ids {
			scoped := tc.base
			if tc.dimension == "source" {
				scoped.SourceID = id
			} else {
				scoped.Category = id
			}
			result, err := svc.PromptGallerySearch(scoped)
			if err != nil {
				t.Fatalf("%s %s: %v", tc.name, id, err)
			}
			shown := baseSources[id]
			if tc.dimension == "category" {
				shown = baseCategories[id]
			}
			if result.Total != shown {
				t.Fatalf("%s: %s 下拉标 %d，选中只筛出 %d", tc.name, id, shown, result.Total)
			}
		}
	}
}

func TestPromptGallerySourcesReportSyncState(t *testing.T) {
	svc := newPromptGalleryTestService(t)
	syncedAt := time.Now().UTC()
	if err := svc.repo.UpsertPromptGallerySource(&model.PromptGallerySource{
		ID: "hf-seedance-2", Name: "Seedance 2 提示词数据集", License: "CC BY 4.0",
		Revision: "hf-resolve-main", TotalRows: 8834, LastSyncedAt: &syncedAt,
	}); err != nil {
		t.Fatal(err)
	}
	sources, err := svc.PromptGallerySources()
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 {
		t.Fatalf("expected both registered sources, got %d", len(sources))
	}
	byID := map[string]PromptGallerySourceView{}
	for _, source := range sources {
		byID[source.ID] = source
	}
	if byID["hf-seedance-2"].TotalRows != 8834 || byID["hf-seedance-2"].LastSyncedAt == nil {
		t.Fatalf("synced source state wrong: %#v", byID["hf-seedance-2"])
	}
	// 未同步的来源仍然出现在列表里（前端据此显示"待同步"）。
	if byID["hf-gpt-image-2"].TotalRows != 0 || byID["hf-gpt-image-2"].LastSyncedAt != nil {
		t.Fatalf("unsynced source state wrong: %#v", byID["hf-gpt-image-2"])
	}
}

func TestStartPromptGallerySyncRejectsUnknownSource(t *testing.T) {
	svc := newPromptGalleryTestService(t)
	if err := svc.StartPromptGallerySync("not-a-source"); err == nil {
		t.Fatal("unknown source accepted")
	}
}
func TestPromptGalleryUpsertBatchesLargeSyncs(t *testing.T) {
svc := newPromptGalleryTestService(t)
syncedAt := time.Now().UTC()
rows := make([]model.PromptGalleryPrompt, 0, 500)
for index := 0; index < 500; index++ {
rows = append(rows, model.PromptGalleryPrompt{
ID: fmt.Sprintf("hf-seedance-2:%d", index), SourceID: "hf-seedance-2", ExternalID: fmt.Sprintf("%d", index),
Mode: "video", Title: fmt.Sprintf("标题 %d", index), Prompt: "prompt body", SyncedAt: syncedAt,
})
}
// 500 行超过单条语句的批大小，必须切分写入而不是报 "too many SQL variables"。
if err := svc.repo.UpsertPromptGalleryPrompts(rows); err != nil {
t.Fatalf("batch upsert failed: %v", err)
}
result, err := svc.PromptGallerySearch(PromptGallerySearchRequest{Limit: 1})
if err != nil {
t.Fatal(err)
}
if result.Total != 500 {
t.Fatalf("total = %d, want 500", result.Total)
}

// 重复同步同一批是幂等的（靠 source_id + external_id 唯一索引），不会翻倍。
if err := svc.repo.UpsertPromptGalleryPrompts(rows); err != nil {
t.Fatal(err)
}
again, err := svc.PromptGallerySearch(PromptGallerySearchRequest{Limit: 1})
if err != nil {
t.Fatal(err)
}
if again.Total != 500 {
t.Fatalf("re-sync duplicated rows: total = %d", again.Total)
}
}

// 回归：清理过期行若用了比写入更晚的时间戳，会把刚写入的行删空——
// 表现是同步报成功、库里 0 行。导入两轮后必须仍有数据。
func TestPromptGalleryImportKeepsItsOwnRows(t *testing.T) {
svc := newPromptGalleryTestService(t)
descriptor, _ := promptGallerySourceByID("hf-seedance-2")

imported, _, err := svc.importPromptGalleryJSONL(descriptor, strings.NewReader(promptGallerySampleJSONL), 0)
if err != nil {
t.Fatal(err)
}
if imported != 2 {
t.Fatalf("imported = %d, want 2", imported)
}
assertStored := func(stage string) {
result, err := svc.PromptGallerySearch(PromptGallerySearchRequest{SourceID: "hf-seedance-2", Limit: 10})
if err != nil {
t.Fatal(err)
}
if result.Total != 2 {
t.Fatalf("%s: stored rows = %d, want 2", stage, result.Total)
}
}
assertStored("first import")

// 第二轮同步（模拟重新拉取）不能把上一轮的行当成过期数据删掉。
if _, _, err := svc.importPromptGalleryJSONL(descriptor, strings.NewReader(promptGallerySampleJSONL), 0); err != nil {
t.Fatal(err)
}
assertStored("second import")
}
