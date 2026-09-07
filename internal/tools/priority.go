package tools

import "sort"

// PriorityQueue is a simple priority queue implementation with FIFO per Priority.
type PriorityQueue[T any] struct {
	queues     map[int][]T
	priorities []int
}

func NewPriorityQueue[T any]() *PriorityQueue[T] {
	return &PriorityQueue[T]{
		queues:     make(map[int][]T),
		priorities: make([]int, 0),
	}
}

func (pq *PriorityQueue[T]) Enqueue(priority int, value T) {
	if _, exists := pq.queues[priority]; !exists {
		pq.priorities = append(pq.priorities, priority)
		sort.Ints(pq.priorities)
	}
	pq.queues[priority] = append(pq.queues[priority], value)
}

func (pq *PriorityQueue[T]) Dequeue() (T, bool) {
	var zero T
	if pq.IsEmpty() {
		return zero, false
	}
	priority := pq.priorities[0]
	queue := pq.queues[priority]
	if len(queue) == 0 {
		return zero, false
	}
	value := queue[0]
	if len(queue) == 1 {
		delete(pq.queues, priority)
		pq.priorities = pq.priorities[1:]
	} else {
		pq.queues[priority] = queue[1:]
	}
	return value, true
}

func (pq *PriorityQueue[T]) IsEmpty() bool {
	return len(pq.priorities) == 0
}

func (pq *PriorityQueue[T]) Extract() []T {
	var result []T
	for _, priority := range pq.priorities {
		result = append(result, pq.queues[priority]...)
	}
	return result
}
