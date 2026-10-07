package main

// Лента событий личного кабинета.
//
// POST /EventNotifications/GetNotifications  тело {"pageIndex":1,"pageSize":N}
// Важно: параметр isRead передавать НЕЛЬЗЯ — сервер отвечает внутренней
// ошибкой. pageIndex начинается с 1 (при 0 портал ругается на OFFSET).
//
// Уведомления приходят по трём типам событий, ровно по нашим каталогам:
//   FedsfmPortal.NewTerroristCatalog — перечень по экстремизму/терроризму
//   FedsfmPortal.NewMvkCatalog       — перечень МВК
//   FedsfmPortal.SbOonCatalog        — перечни Совета Безопасности ООН
//
// По лентам сообщений («Решения судов», «Информация») уведомлений не бывает —
// их свежесть определяется иначе, см. feeds.go.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Notification — одно уведомление кабинета.
type Notification struct {
	ID         string `json:"idNotification"`
	Title      string `json:"notificationTitle"`
	CreateDate string `json:"createDate"`
	IsRead     bool   `json:"isRead"`
	Event      struct {
		EventTypeCode string `json:"eventTypeCode"`
	} `json:"event"`
}

type notificationsResponse struct {
	IsError bool   `json:"isError"`
	Message string `json:"message"`
	Data    struct {
		Notifications []Notification `json:"notifications"`
		RecordTotal   int            `json:"recordTotal"`
		UnreadCount   int            `json:"unreadbleCount"`
	} `json:"data"`
}

// eventCodeToSection связывает тип события с разделом выгрузки.
var eventCodeToSection = map[string]string{
	"FedsfmPortal.NewTerroristCatalog": "terrorism",
	"FedsfmPortal.NewMvkCatalog":       "mvk",
	"FedsfmPortal.SbOonCatalog":        "oon",
}

// Notifications забирает ленту событий кабинета.
func (c *Client) Notifications(pageSize int) ([]Notification, error) {
	if pageSize <= 0 {
		pageSize = 200
	}
	body, err := json.Marshal(map[string]any{
		"pageIndex": 1,
		"pageSize":  pageSize,
	})
	if err != nil {
		return nil, err
	}

	req, err := c.newRequest(http.MethodPost, "/EventNotifications/GetNotifications", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("уведомления: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("уведомления: статус %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("уведомления: чтение ответа: %w", err)
	}
	if isHTML(raw) {
		return nil, fmt.Errorf("уведомления: получен HTML вместо JSON (сессия истекла)")
	}

	var parsed notificationsResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("уведомления: разбор ответа: %w", err)
	}
	if parsed.IsError {
		return nil, fmt.Errorf("уведомления: портал вернул ошибку: %s", parsed.Message)
	}
	return parsed.Data.Notifications, nil
}

// LatestBySection возвращает по каждому разделу дату самого свежего
// уведомления и список id всех уведомлений этого раздела.
func LatestBySection(items []Notification) (latest map[string]string, ids map[string][]string) {
	latest = map[string]string{}
	ids = map[string][]string{}
	for _, n := range items {
		section, ok := eventCodeToSection[n.Event.EventTypeCode]
		if !ok {
			continue
		}
		ids[section] = append(ids[section], n.ID)
		// createDate приходит в ISO-виде, поэтому строки сравнимы лексикографически.
		if n.CreateDate > latest[section] {
			latest[section] = n.CreateDate
		}
	}
	return latest, ids
}

// UnreadIDs отбирает из списка непрочитанные уведомления.
func UnreadIDs(items []Notification, wanted []string) []string {
	want := map[string]bool{}
	for _, id := range wanted {
		want[id] = true
	}
	var out []string
	for _, n := range items {
		if want[n.ID] && !n.IsRead {
			out = append(out, n.ID)
		}
	}
	return out
}

// MarkRead помечает прочитанными конкретные уведомления.
// Вызывается только после успешного скачивания соответствующего раздела —
// так отметка повторяет поведение человека, который открыл кабинет,
// увидел новое и забрал его.
func (c *Client) MarkRead(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	body, err := json.Marshal(ids)
	if err != nil {
		return err
	}

	req, err := c.newRequest(http.MethodPost, "/EventNotifications/GetCheckedNotifications", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("отметка прочитанным: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("отметка прочитанным: статус %d", resp.StatusCode)
	}
	return nil
}
