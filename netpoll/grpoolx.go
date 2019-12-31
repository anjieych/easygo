package netpoll

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

func GoID() int {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	idField := strings.Fields(strings.TrimPrefix(string(buf[:n]), "goroutine "))[0]
	id, err := strconv.Atoi(idField)
	if err != nil {
		panic(fmt.Sprintf("cannot get goroutine id: %v", err))
	}
	return id
}

// ErrScheduleTimeout returned by Grpool to indicate that there no free
// goroutines during some period of time.
var ErrScheduleTimeout = fmt.Errorf("schedule error: timed out")

type Task struct {
	fn func(e EpollEvent)
	e  EpollEvent
}
type Grpoolx struct {
	work  chan *Task
	idle  time.Duration
	busy  time.Duration
	min   int
	max   int
	count int
	sync.RWMutex
	timerpool *Timerpool
	taskpool  *Taskpool
}

// NewGrpoolx create new Grpoolx  with min goroutines initionated, and max goroutine limited.
// When a goroutine was idled ,then kill it.
// When all goroutines are buse and count between min and max ,a task will be scheduled to a new goroutine,or get  ErrScheduleTimeout
func NewGrpoolx(max int, min int, idle time.Duration, busy time.Duration) *Grpoolx {
	if min <= 0 {
		panic("Grpoolx require min >0")
	} else if min > max {
		panic("Grpool require max >= min")
	} else if idle < 1*time.Second {
		panic("Grpool require idle > 1*time.Second")
	} else if busy < 1*time.Millisecond {
		panic("Grpool require busy > 1*time.Millisecond")
	} else {
		// no code here!!!
	}

	g := &Grpoolx{idle: idle, busy: busy, min: min, max: max, count: 0, timerpool: &Timerpool{}, taskpool: &Taskpool{}}

	g.work = make(chan *Task, max)

	var task *Task
	for i := 0; i < min; i++ {
		task = g.taskpool.Get(nil, 0)
		go g.dowork(task)
	}
	return g
}

func (g *Grpoolx) Schedule(task *Task) error {
	return g.schedule(nil, task)
}

func (g *Grpoolx) ScheduleTimeout(timeout time.Duration, task *Task) error {
	//timer := time.NewTimer(timeout)
	timer := g.timerpool.Get(timeout)
	defer g.timerpool.Put(timer)

	return g.schedule(timer.C, task)
}

func (g *Grpoolx) schedule(timeout <-chan time.Time, task *Task) error {
	//busy := time.NewTimer(g.busy)
	busy := g.timerpool.Get(g.busy)
	defer g.timerpool.Put(busy)

	select {
	case <-timeout:
		g.taskpool.Put(task)
		return ErrScheduleTimeout
	case g.work <- task:
		return nil
	case <-busy.C:
		g.RLock()
		more := g.max - g.count
		g.RUnlock()
		if more > 0 {
			if more > 15 {
				go g.dowork(task)
				var t *Task
				for i := 0; i < 9; i++ {
					t = g.taskpool.Get(nil, 0)
					go g.dowork(t)
				}
			} else {
				go g.dowork(task)
			}
			return nil
		} else {
			select {
			case <-timeout:
				g.taskpool.Put(task)
				return ErrScheduleTimeout
			case g.work <- task:
				return nil
			}
		}
	}
}

// dowork execute task ,if trigger idle ,then kill current goroutine
func (g *Grpoolx) dowork(task *Task) {
	g.Lock()
	g.count = g.count + 1
	g.Unlock()

	if task.fn != nil {
		task.fn(task.e)
	}

	//idle := time.NewTimer(g.idle)
	idle := g.timerpool.Get(g.idle)
	defer g.timerpool.Put(idle)
loop:
	for {
		select {
		case task = <-g.work:
			task.fn(task.e)
			// 回收 task
			g.taskpool.Put(task)
		case <-idle.C:
			g.RLock()
			more := g.count - g.min
			g.RUnlock()
			if more > 0 {
				break loop
			}
		}
		idle.Reset(g.idle)
	}
	g.Lock()
	g.count = g.count - 1
	g.Unlock()

	runtime.Goexit()
}

/*+++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++*/

// Timerpool provides GC-able pooling of *time.Timer's.
// can be used by multiple goroutines concurrently.
type Timerpool struct {
	p sync.Pool
}

// Get returns a timer that completes after the given duration.
func (tp *Timerpool) Get(d time.Duration) *time.Timer {
	if t, _ := tp.p.Get().(*time.Timer); t != nil {
		t.Reset(d)
		return t
	}

	return time.NewTimer(d)
}

// Put pools the given timer.
//
// There is no need to call t.Stop() before calling Put.
//
// Put will try to stop the timer before pooling. If the
// given timer already expired, Put will read the unreceived
// value if there is one.
func (tp *Timerpool) Put(t *time.Timer) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}

	tp.p.Put(t)
}

/*+++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++*/

// Timerpool provides GC-able pooling of *time.Timer's.
// can be used by multiple goroutines concurrently.
type Taskpool struct {
	p sync.Pool
}

// Get returns a timer that completes after the given duration.
func (tp *Taskpool) Get(fn func(EpollEvent), e EpollEvent) *Task {
	if t, _ := tp.p.Get().(*Task); t != nil {
		t.fn = fn
		t.e = e
		return t
	}

	return &Task{fn: fn, e: e}
}

// Put pools the given timer.
//
// There is no need to call t.Stop() before calling Put.
//
// Put will try to stop the timer before pooling. If the
// given timer already expired, Put will read the unreceived
// value if there is one.
func (tp *Taskpool) Put(t *Task) {
	t.fn = nil
	t.e = 0

	tp.p.Put(t)
}
