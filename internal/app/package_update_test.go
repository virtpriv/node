package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"
)

type packageSession struct {
	step   func(int) error
	end    func() error
	closed bool
}

func (s *packageSession) WaitStep(i int) error { return s.step(i) }
func (s *packageSession) Wait(any) error       { return s.end() }
func (s *packageSession) Close()               { s.closed = true }

func TestPackageUpdateRequiresFinalCompletion(t *testing.T) {
	failure := errors.New("injected helper failure")
	for _, fail := range []int{-1, 0, 1, 2, 3} {
		t.Run(map[int]string{-1: "complete", 0: "refresh", 1: "upgrade", 2: "final audit or lost terminator", 3: "connection"}[fail], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				u := NewPackageUpdates()
				defer u.Close()
				var steps []int
				end := make(chan struct{})
				session := &packageSession{step: func(i int) error {
					steps = append(steps, i)
					if fail == i {
						return failure
					}
					return nil
				}, end: func() error {
					<-end
					if fail == 2 {
						return failure
					}
					return nil
				}}
				calls := 0
				u.start = func(_ context.Context, verb string, params any) (helperSession, error) {
					calls++
					if verb != "package-update" || params != nil {
						t.Errorf("wrong request: %s %v", verb, params)
					}
					if fail == 3 {
						return nil, failure
					}
					return session, nil
				}
				results := u.Update()
				synctest.Wait()
				if fail == -1 || fail == 2 {
					select {
					case result := <-results:
						t.Fatalf("reported completion before final audit: %+v", result)
					default:
					}
				}
				close(end)
				synctest.Wait()
				result := <-results
				want := PackageUpdateUnconfirmed
				wantErr := failure
				if fail == -1 {
					want = PackageUpdateCompleted
					wantErr = nil
				}
				if result.Outcome != want || calls != 1 || !errors.Is(result.Err, wantErr) {
					t.Fatalf("result %+v, calls %d", result, calls)
				}
				wantSteps := []int{0, 1}
				if fail == 0 {
					wantSteps = []int{0}
				}
				if fail == 3 {
					wantSteps = nil
				}
				if !reflect.DeepEqual(steps, wantSteps) || (fail != 3 && !session.closed) {
					t.Fatalf("steps %v, closed %v", steps, session.closed)
				}
				if _, ok := <-results; ok {
					t.Fatal("duplicate result")
				}
			})
		})
	}
}

func TestPackageUpdateObservationBoundAndClose(t *testing.T) {
	for _, closeEarly := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "close joins without consumer"}[closeEarly], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				u := NewPackageUpdates()
				defer u.Close()
				release := make(chan struct{})
				calls := 0
				session := &packageSession{}
				u.start = func(ctx context.Context, _ string, _ any) (helperSession, error) {
					calls++
					session.step = func(int) error {
						<-ctx.Done()
						if closeEarly {
							<-release
						}
						return ctx.Err()
					}
					return session, nil
				}
				started := time.Now()
				results := u.Update()
				wantErr := context.DeadlineExceeded
				if closeEarly {
					wantErr = context.Canceled
					synctest.Wait()
					closed := make(chan struct{})
					go func() { u.Close(); close(closed) }()
					synctest.Wait()
					select {
					case <-closed:
						t.Fatal("Close did not join observation")
					default:
					}
					close(release)
					<-closed
				}
				result := <-results
				if result.Outcome != PackageUpdateUnconfirmed || !errors.Is(result.Err, wantErr) || calls != 1 || !session.closed {
					t.Fatalf("result %+v, calls %d, closed %v", result, calls, session.closed)
				}
				if !closeEarly && time.Since(started) != 95*time.Minute {
					t.Fatal("wrong deadline")
				}
				u.Close()
				if result := <-u.Update(); result.Outcome != PackageUpdateNotStarted || calls != 1 {
					t.Fatal("accepted work after shutdown")
				}
			})
		})
	}
}
