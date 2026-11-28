package session

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// stubMongoTotal counts how each counting strategy was called, so a test can
// assert which one a page used instead of only looking at the number.
type stubMongoTotal struct {
	estimatedCalls int
	exactCalls     int
	estimatedTotal int64
	exactTotal     int64
}

func (s *stubMongoTotal) CountDocuments(context.Context, interface{}, ...*options.CountOptions) (int64, error) {
	s.exactCalls++
	return s.exactTotal, nil
}

func (s *stubMongoTotal) EstimatedDocumentCount(context.Context, ...*options.EstimatedDocumentCountOptions) (int64, error) {
	s.estimatedCalls++
	return s.estimatedTotal, nil
}

// A first page over an unfiltered collection must not run a full
// CountDocuments scan: the collection metadata estimate is enough. Pages of
// the same query after the first must not count at all.
func TestMongoCountPageUsesCheapestStrategy(t *testing.T) {
	ctx := context.Background()

	t.Run("empty filter first page uses the estimate", func(t *testing.T) {
		s := NewMongoSession("mongo-count-estimate")
		stub := &stubMongoTotal{estimatedTotal: 42000, exactTotal: 42000}

		total, err := s.countPage(ctx, stub, mongoTotalKey("db", "c", ""), nil, true, 0)
		if err != nil {
			t.Fatalf("countPage: %v", err)
		}
		if total != 42000 {
			t.Errorf("total = %d, want 42000", total)
		}
		if stub.estimatedCalls != 1 || stub.exactCalls != 0 {
			t.Errorf("first page with an empty filter: estimated=%d exact=%d, want estimated=1 exact=0 (a full scan per page)", stub.estimatedCalls, stub.exactCalls)
		}
	})

	t.Run("later pages reuse the total", func(t *testing.T) {
		s := NewMongoSession("mongo-count-reuse")
		stub := &stubMongoTotal{estimatedTotal: 42000, exactTotal: 42000}

		if _, err := s.countPage(ctx, stub, mongoTotalKey("db", "c", ""), nil, true, 0); err != nil {
			t.Fatalf("countPage page 1: %v", err)
		}
		if _, err := s.countPage(ctx, stub, mongoTotalKey("db", "c", ""), nil, true, 100); err != nil {
			t.Fatalf("countPage page 2: %v", err)
		}
		if stub.estimatedCalls+stub.exactCalls != 1 {
			t.Errorf("two pages caused %d counts, want 1 (only the first page counts)", stub.estimatedCalls+stub.exactCalls)
		}
	})

	t.Run("filtered query keeps the exact count", func(t *testing.T) {
		s := NewMongoSession("mongo-count-exact")
		stub := &stubMongoTotal{estimatedTotal: 7, exactTotal: 3}
		filter := bson.M{"status": "active"}

		total, err := s.countPage(ctx, stub, mongoTotalKey("db", "c", `{"status":"active"}`), filter, false, 0)
		if err != nil {
			t.Fatalf("countPage: %v", err)
		}
		if total != 3 {
			t.Errorf("total = %d, want 3", total)
		}
		if stub.exactCalls != 1 || stub.estimatedCalls != 0 {
			t.Errorf("filtered first page: exact=%d estimated=%d, want exact=1 estimated=0", stub.exactCalls, stub.estimatedCalls)
		}
	})

}
func TestBuildMongoURI(t *testing.T) {
	cases := []struct {
		name   string
		config ConnectionConfig
		want   string
	}{
		{
			name:   "defaults get authSource and directConnection",
			config: ConnectionConfig{Host: "10.0.0.1", Port: 27017},
			want:   "mongodb://10.0.0.1:27017?authSource=admin&directConnection=true",
		},
		{
			name:   "credentials are percent-escaped",
			config: ConnectionConfig{Host: "10.0.0.1", Port: 27017, User: "u@er", Password: "p/a#ss:word"},
			want:   "mongodb://u%40er:p%2Fa%23ss%3Aword@10.0.0.1:27017?authSource=admin&directConnection=true",
		},
		{
			name:   "dbname lands in path",
			config: ConnectionConfig{DBName: "mydb"},
			want:   "mongodb://127.0.0.1:27017/mydb?authSource=admin&directConnection=true",
		},
		{
			name:   "user authSource overrides the admin default without duplication",
			config: ConnectionConfig{DBParams: "authSource=mydb"},
			want:   "mongodb://127.0.0.1:27017?authSource=mydb&directConnection=true",
		},
		{
			name:   "replicaSet disables the directConnection default",
			config: ConnectionConfig{DBParams: "replicaSet=rs0"},
			want:   "mongodb://127.0.0.1:27017?authSource=admin&replicaSet=rs0",
		},
		{
			name:   "explicit directConnection is preserved",
			config: ConnectionConfig{DBParams: "directConnection=false"},
			want:   "mongodb://127.0.0.1:27017?authSource=admin&directConnection=false",
		},
		{
			name:   "extra params are merged alongside the defaults",
			config: ConnectionConfig{DBParams: "ssl=true&connectTimeoutMS=5000"},
			want:   "mongodb://127.0.0.1:27017?authSource=admin&connectTimeoutMS=5000&directConnection=true&ssl=true",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildMongoURI(tc.config)
			if got != tc.want {
				t.Errorf("buildMongoURI(%+v)\n  got:  %s\n  want: %s", tc.config, got, tc.want)
			}
		})
	}
}
