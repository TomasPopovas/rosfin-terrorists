package main

// Состояние между прогонами: что мы уже забирали и на какую дату.
// Хранится в downloads/state.json рядом с папками выгрузок.
//
// Именно state.json — источник истины при решении «качать или нет».
// Флаг isRead в кабинете для этого не годится: человек может открыть
// уведомление руками раньше робота, и тогда обновление было бы пропущено.

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const stateFileName = "state.json"

// State — последние обработанные отметки времени.
type State struct {
	// Catalogs: раздел -> createDate последнего учтённого уведомления
	// в формате, который отдаёт портал ("2026-09-14T16:41:01.803").
	Catalogs map[string]string `json:"catalogs"`
	// Feeds: имя ленты -> дата последнего учтённого сообщения
	// в формате грида ("20.09.2022 11:11").
	Feeds map[string]string `json:"feeds"`
}

func NewState() State {
	return State{
		Catalogs: map[string]string{},
		Feeds:    map[string]string{},
	}
}

// LoadState читает state.json. Отсутствие файла — не ошибка:
// это первый запуск, возвращаем пустое состояние.
func LoadState(base string) (State, error) {
	path := filepath.Join(base, stateFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewState(), nil
		}
		return NewState(), err
	}
	s := NewState()
	if err := json.Unmarshal(data, &s); err != nil {
		// Битый state.json не должен блокировать работу: начинаем с чистого.
		return NewState(), err
	}
	if s.Catalogs == nil {
		s.Catalogs = map[string]string{}
	}
	if s.Feeds == nil {
		s.Feeds = map[string]string{}
	}
	return s, nil
}

// SaveState записывает state.json целиком.
func SaveState(base string, s State) error {
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(base, stateFileName), data)
}
