package sessionruntime

import "sync"

const maxRetainedEvents = 4096

// Journal keeps the replayable event stream for the live provider process.
type Journal struct {
	mu          sync.Mutex
	maxEvents   int
	events      []Event
	firstEvent  int
	nextID      int64
	subscribers map[int]*journalSubscriber
	nextSubID   int
}

type journalSubscriber struct {
	events   chan Event
	overflow chan struct{}
}

func NewJournal() *Journal {
	return newJournal(maxRetainedEvents)
}

func newJournal(maxEvents int) *Journal {
	return &Journal{
		maxEvents:   maxEvents,
		nextID:      1,
		subscribers: map[int]*journalSubscriber{},
	}
}

// Append records and broadcasts one event.
func (j *Journal) Append(event Event) {
	j.mu.Lock()
	defer j.mu.Unlock()

	event.ID = j.nextID
	j.nextID++
	j.events = append(j.events, event)
	if len(j.events)-j.firstEvent > j.maxEvents {
		j.events[j.firstEvent] = Event{}
		j.firstEvent++
		if j.firstEvent == j.maxEvents {
			// Compact in batches so streamed deltas do not copy the full history per event.
			j.events = append([]Event(nil), j.events[j.firstEvent:]...)
			j.firstEvent = 0
		}
	}
	for id, subscriber := range j.subscribers {
		select {
		case subscriber.events <- event:
		default:
			close(subscriber.events)
			close(subscriber.overflow)
			delete(j.subscribers, id)
		}
	}
}

// Subscribe returns retained events, a live stream, an overflow signal, and a cancel function.
func (j *Journal) Subscribe(since int64) ([]Event, <-chan Event, <-chan struct{}, func()) {
	j.mu.Lock()
	defer j.mu.Unlock()

	retained := make([]Event, 0, len(j.events)-j.firstEvent)
	for _, event := range j.events[j.firstEvent:] {
		if event.ID > since {
			retained = append(retained, event)
		}
	}

	id := j.nextSubID
	j.nextSubID++
	subscriber := &journalSubscriber{events: make(chan Event, 256), overflow: make(chan struct{})}
	j.subscribers[id] = subscriber
	cancel := func() {
		j.mu.Lock()
		defer j.mu.Unlock()
		if subscriber, ok := j.subscribers[id]; ok {
			close(subscriber.events)
			delete(j.subscribers, id)
		}
	}
	return retained, subscriber.events, subscriber.overflow, cancel
}

// Close closes all subscriptions.
func (j *Journal) Close() {
	j.mu.Lock()
	defer j.mu.Unlock()
	for id, subscriber := range j.subscribers {
		close(subscriber.events)
		delete(j.subscribers, id)
	}
}
