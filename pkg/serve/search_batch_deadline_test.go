package serve

import (
	"context"
	"testing"
	"time"

	"github.com/nohles/go-toolkit/pkg/fetcher"
	"github.com/nohles/go-toolkit/pkg/manifest"
	"github.com/nohles/go-toolkit/pkg/pub"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// slowSearchService blocks "slow" queries until their context expires and
// answers every other query immediately with one result.
type slowSearchService struct{}

func (slowSearchService) Links() manifest.LinkList { return nil }
func (slowSearchService) Get(context.Context, manifest.Link) (fetcher.Resource, bool) {
	return nil, false
}
func (slowSearchService) Close()                     {}
func (slowSearchService) Options() pub.SearchOptions { return pub.SearchOptions{} }
func (slowSearchService) Search(ctx context.Context, query string) (pub.SearchIterator, error) {
	return &slowIterator{slow: query == "slow"}, nil
}

type slowIterator struct {
	slow bool
	done bool
}

func (it *slowIterator) Total() (int, bool) { return 1, true }
func (it *slowIterator) Next(ctx context.Context) (pub.LocatorCollectionPage, error) {
	if it.slow {
		<-ctx.Done()
		return pub.LocatorCollectionPage{}, ctx.Err()
	}
	if it.done {
		return pub.LocatorCollectionPage{}, pub.ErrIteratorDone
	}
	it.done = true
	return pub.LocatorCollectionPage{Locators: []manifest.Locator{{}}, Number: 1}, nil
}
func (it *slowIterator) Close() {}

func TestBatchTermsEachGetTheirOwnDeadline(t *testing.T) {
	results := runBatchTerms(context.Background(), slowSearchService{}, []batchTerm{
		{ID: "a", Text: "slow"},
		{ID: "b", Text: "slow"},
		{ID: "c", Text: "fast"},
	}, 20*time.Millisecond)

	assert.Equal(t, "timeout", results["a"].Error)
	assert.Equal(t, "timeout", results["b"].Error)
	require.Empty(t, results["c"].Error, "a term after slow ones must not inherit their spent deadline")
	assert.Len(t, results["c"].Locators, 1)
}

func TestBatchTermsStopWhenTheClientCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results := runBatchTerms(ctx, slowSearchService{}, []batchTerm{{ID: "a", Text: "slow"}}, time.Minute)
	assert.Equal(t, "timeout", results["a"].Error)
}
