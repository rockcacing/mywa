package hub

import "testing"

func TestPublishFiltersBySession(t *testing.T) {
	h := New()
	all := h.Subscribe("")
	one := h.Subscribe("a")
	h.Publish("a", MessageReceived, 1)
	h.Publish("b", MessageReceived, 2)

	if ev := <-all.C; ev.Session != "a" || ev.Data != 1 {
		t.Fatalf("all[0] = %+v", ev)
	}
	if ev := <-all.C; ev.Session != "b" {
		t.Fatalf("all[1] = %+v", ev)
	}
	if ev := <-one.C; ev.Session != "a" {
		t.Fatalf("one[0] = %+v", ev)
	}
	select {
	case ev := <-one.C:
		t.Fatalf("filtered subscriber got %+v", ev)
	default:
	}
}

func TestSlowSubscriberDropped(t *testing.T) {
	h := New()
	slow := h.Subscribe("")
	for i := 0; i < BufferSize+1; i++ {
		h.Publish("a", MessageReceived, i)
	}
	if h.Len() != 0 {
		t.Fatal("slow subscriber not removed")
	}
	n := 0
	for range slow.C { // drains the buffer, then sees the channel closed
		n++
	}
	if n != BufferSize {
		t.Fatalf("drained %d events, want %d", n, BufferSize)
	}
	h.Unsubscribe(slow) // second close must not panic
}
