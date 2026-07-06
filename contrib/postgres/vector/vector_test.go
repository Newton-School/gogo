package vector

import (
	"testing"

	modelfields "github.com/Newton-School/gogo/models/fields"
)

func TestVectorFieldAndIndexMetadata(t *testing.T) {
	field := FieldMeta("embedding", "embedding", 384)
	if field.Name != "embedding" || field.Column != "embedding" || field.Kind != "vector(384)" {
		t.Fatalf("field metadata = %#v", field)
	}

	custom := NewField(modelfields.Options{Name: "embedding", Column: "embedding"}, 384)
	if got := custom.ColumnType("postgres"); got != "vector(384)" {
		t.Fatalf("custom field postgres column type = %q, want vector(384)", got)
	}

	hnsw := HNSWIndex("embedding_hnsw_idx", "embedding", CosineOps)
	if hnsw.Name != "embedding_hnsw_idx" || hnsw.Method != "hnsw" || hnsw.OpClasses[0] != string(CosineOps) || hnsw.Fields[0].OpClass != string(CosineOps) {
		t.Fatalf("hnsw index = %#v", hnsw)
	}

	ivf := IVFFlatIndex("embedding_ivf_idx", "embedding", L2Ops)
	if ivf.Method != "ivfflat" || ivf.OpClasses[0] != string(L2Ops) || ivf.Fields[0].OpClass != string(L2Ops) {
		t.Fatalf("ivfflat index = %#v", ivf)
	}
}

func TestVectorHelpersRejectInvalidInput(t *testing.T) {
	if got := FieldMeta("embedding", "embedding", 0); got.Kind != "" {
		t.Fatalf("invalid dimension field = %#v", got)
	}
	if got := HNSWIndex("idx", "", CosineOps); len(got.Fields) != 0 {
		t.Fatalf("empty field index = %#v", got)
	}
}
