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
