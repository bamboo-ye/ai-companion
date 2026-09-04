package eventbus

import "context"

const DatabaseReconciledTopic = "database.reconciled"

// DatabaseReconciledPublisher settles transactional Outbox rows after the
// durable domain record has become the database dispatch source. The matching
// lease-based reconcilers execute that domain record independently; no event
// payload is discarded before durable state exists because both rows are
// created in the same business transaction.
type DatabaseReconciledPublisher struct{}

func (DatabaseReconciledPublisher) Publish(ctx context.Context, event Event) (PublishAck, error) {
	if err := ctx.Err(); err != nil {
		return PublishAck{}, err
	}
	if _, err := TopicFor(event.Type); err != nil {
		return PublishAck{}, err
	}
	return PublishAck{Topic: DatabaseReconciledTopic, Partition: 0, Offset: 0}, nil
}
