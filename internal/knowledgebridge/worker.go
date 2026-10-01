package knowledgebridge

import (
	"context"
	"fmt"
	"github.com/windcry1/ai-companion/internal/document"
	"github.com/windcry1/ai-companion/internal/memory"
	"github.com/windcry1/ai-companion/internal/semantic"
	"sync"
	"time"
)

type Owners interface {
	ListKnowledgeOwners(context.Context, string, int) ([]string, error)
}

// Reconcile discovers work from authoritative domain rows. This closes the
// crash window between a domain commit and optional eager queueing.
func Reconcile(ctx context.Context, owners Owners, d *document.Service, m *memory.Service, c *semantic.Client) error {
	cursor := ""
	for {
		page, err := owners.ListKnowledgeOwners(ctx, cursor, 128)
		if err != nil {
			return err
		}
		for _, owner := range page {
			if err = d.QueueDocumentIndexes(ctx, owner); err != nil {
				return err
			}
			if err = m.IndexKnowledge(ctx, owner); err != nil {
				return err
			}
			if err = d.IndexKnowledge(ctx, owner); err != nil {
				return err
			}
		}
		if len(page) < 128 {
			break
		}
		nextCursor := page[len(page)-1]
		if nextCursor <= cursor {
			return fmt.Errorf("knowledge owner cursor did not advance")
		}
		cursor = nextCursor
	}
	// Prune only after a successful complete scan; outages never erase the index.
	_, err := c.PruneProjections(ctx, time.Now().UTC().Add(-24*time.Hour))
	return err
}

// Reconciliation, document projection, and semantic projection are bounded
// independently so a long document cannot stall memory freshness or cleanup.
func RunProjections(ctx context.Context, owners Owners, d *document.Service, m *memory.Service, c *semantic.Client, onError func(error)) error {
	var workers sync.WaitGroup
	loop := func(process func(context.Context) (bool, error)) {
		defer workers.Done()
		for ctx.Err() == nil {
			worked, err := process(ctx)
			if err != nil && onError != nil && ctx.Err() == nil {
				onError(err)
			}
			if worked && err == nil {
				continue
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
	workers.Add(2)
	go loop(d.ProcessDocumentIndex)
	valid := ValidateProjection(d, m, c)
	go loop(func(ctx context.Context) (bool, error) { return c.ProcessProjection(ctx, valid) })
	defer workers.Wait()
	for ctx.Err() == nil {
		if err := Reconcile(ctx, owners, d, m, c); err != nil && onError != nil && ctx.Err() == nil {
			onError(err)
		}
		timer := time.NewTimer(time.Minute)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
}
