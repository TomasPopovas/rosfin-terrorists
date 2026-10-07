package main

// Планировщик по окнам: вместо фиксированного интервала запуск происходит
// раз в день в каждом из заданных окон времени, в случайную минуту внутри
// окна (чтобы не бить строго по одному и тому же времени).
//
// Формат окна: "09:00-10:00". Несколько окон — через запятую:
// "09:00-10:00,17:00-18:00".

import (
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"
)

// Window — одно окно времени в течение суток (локальное время).
type Window struct {
	StartH, StartM int
	EndH, EndM     int
}

func (w Window) startMinutes() int { return w.StartH*60 + w.StartM }
func (w Window) endMinutes() int   { return w.EndH*60 + w.EndM }

func (w Window) String() string {
	return fmt.Sprintf("%02d:%02d-%02d:%02d", w.StartH, w.StartM, w.EndH, w.EndM)
}

// ParseWindows разбирает строку вида "09:00-10:00,17:00-18:00".
func ParseWindows(raw string) ([]Window, error) {
	var windows []Window
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		w, err := parseWindow(part)
		if err != nil {
			return nil, fmt.Errorf("окно %q: %w", part, err)
		}
		windows = append(windows, w)
	}
	if len(windows) == 0 {
		return nil, errors.New("не задано ни одного окна запуска")
	}
	return windows, nil
}

func parseWindow(s string) (Window, error) {
	bounds := strings.SplitN(s, "-", 2)
	if len(bounds) != 2 {
		return Window{}, errors.New("ожидался формат ЧЧ:ММ-ЧЧ:ММ")
	}
	sh, sm, err := parseClock(bounds[0])
	if err != nil {
		return Window{}, err
	}
	eh, em, err := parseClock(bounds[1])
	if err != nil {
		return Window{}, err
	}
	w := Window{StartH: sh, StartM: sm, EndH: eh, EndM: em}
	if w.endMinutes() <= w.startMinutes() {
		return Window{}, errors.New("конец окна должен быть позже начала")
	}
	return w, nil
}

func parseClock(s string) (int, int, error) {
	s = strings.TrimSpace(s)
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0, errors.New("ожидался формат ЧЧ:ММ")
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return 0, 0, errors.New("часы должны быть от 00 до 23")
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return 0, 0, errors.New("минуты должны быть от 00 до 59")
	}
	return h, m, nil
}

// FormatWindows склеивает окна обратно в строку для логов.
func FormatWindows(windows []Window) string {
	parts := make([]string, len(windows))
	for i, w := range windows {
		parts[i] = w.String()
	}
	return strings.Join(parts, ",")
}

// Scheduler хранит окна и дату последнего запуска по каждому окну,
// чтобы не запускать одно и то же окно дважды за сутки.
type Scheduler struct {
	windows    []Window
	lastRunDay map[int]string
	rnd        *rand.Rand
}

func NewScheduler(windows []Window) *Scheduler {
	return &Scheduler{
		windows:    windows,
		lastRunDay: make(map[int]string),
		rnd:        rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Next возвращает время ближайшего запуска и индекс окна, к которому оно относится.
func (s *Scheduler) Next(now time.Time) (time.Time, int) {
	today := now.Format("2006-01-02")
	var best time.Time
	bestIdx := -1
	for i, w := range s.windows {
		cand := s.randomInWindow(now, w)
		if s.lastRunDay[i] == today || !cand.After(now) {
			// окно на сегодня уже прошло или уже отработано — переносим на завтра
			cand = s.randomInWindow(now.AddDate(0, 0, 1), w)
		}
		if bestIdx == -1 || cand.Before(best) {
			best = cand
			bestIdx = i
		}
	}
	return best, bestIdx
}

// MarkRun отмечает, что окно idx отработано в день момента at.
func (s *Scheduler) MarkRun(idx int, at time.Time) {
	if idx < 0 {
		return
	}
	s.lastRunDay[idx] = at.Format("2006-01-02")
}

func (s *Scheduler) randomInWindow(day time.Time, w Window) time.Time {
	span := w.endMinutes() - w.startMinutes()
	offset := 0
	if span > 0 {
		offset = s.rnd.Intn(span)
	}
	minutes := w.startMinutes() + offset
	midnight := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
	return midnight.Add(time.Duration(minutes) * time.Minute)
}
