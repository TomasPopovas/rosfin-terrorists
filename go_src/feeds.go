package main

// Ленты сообщений личного кабинета.
//
// Свежесть раздела определяется по дате верхнего сообщения в списке:
//   POST /CommandManager/Execute
//   тело {"Id":"<guid раздела>","State":0,"KbObjectType":N,"Parameters":[]}
// Ответ — JSON, в поле Content лежит HTML грида DevExpress. Разбирать
// вёрстку не нужно: достаточно первой даты вида ДД.ММ.ГГГГ ЧЧ:ММ.
//
// Содержимое сообщения и вложения портал отдаёт так:
//     POST /TextMessage/Open   {"messageId":"<guid>","IdMessageType":67}
//     GET  /TextMessage/GetFile?messageId=<guid>&fileId=<guid>
// Но для этого нужен настоящий messageId верхнего сообщения, а его держит
// внутреннее состояние грида DevExpress — в разметке списка его нет
// (ни у court, ни у info). Вытащить его без реверса протокола контрола
// нельзя, поэтому сейчас по обеим лентам мы только замечаем обновление
// и даём в письме прямую ссылку на раздел кабинета (CanFetch=false).
// Код автоматического скачивания оставлен: если найдётся способ получить
// messageId, достаточно включить CanFetch у нужной ленты.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Feed — одна лента сообщений.
type Feed struct {
	Name          string
	Section       string
	Title         string
	PageID        string
	KbObjectType  int
	IdMessageType int
	// CanFetch: умеем ли забирать содержимое автоматически.
	CanFetch bool
}

// DefaultFeeds — отслеживаемые ленты.
// Оба IdMessageType (67 и 11) сняты с живого портала.
var DefaultFeeds = []Feed{
	{
		Name:    "court",
		Section: "court",
		Title:   "Решения судов о приостановлении операций",
		PageID:  "dda4f5fc-31a7-43cb-924a-fbd3844cf340",
		// KbObjectType 3 и IdMessageType 67 проверены на портале и верны.
		KbObjectType:  3,
		IdMessageType: 67,
		// CanFetch=false: в ответе списка нет настоящего messageId —
		// единственное вхождение оказалось шаблоном параметра команды,
		// портал на нём отвечает NullReference. Идентификатор живёт во
		// внутреннем состоянии грида DevExpress, как и у ленты info.
		// Поэтому фиксируем обновление и даём ссылку на кабинет.
		CanFetch: false,
	},
	{
		Name:          "info",
		Section:       "info",
		Title:         "Информация Росфинмониторинга (Новости ПОД/ФТ)",
		PageID:        "ec052028-42c5-4c6a-8ddc-97f5e08e5b26",
		KbObjectType:  4,
		IdMessageType: 11,
		CanFetch:      false,
	},
}

func FeedByName(name string) (Feed, bool) {
	for _, f := range DefaultFeeds {
		if f.Name == name {
			return f, true
		}
	}
	return Feed{}, false
}

// FeedURL собирает прямую ссылку на раздел кабинета.
// Формат совпадает с тем, что портал ставит в адресную строку сам.
func FeedURL(f Feed) string {
	payload := fmt.Sprintf(`{"Id":"%s","State":0,"KbObjectType":%d,"Parameters":[]}`, f.PageID, f.KbObjectType)
	return "https://portal.fedsfm.ru/#" + base64.StdEncoding.EncodeToString([]byte(payload))
}

type commandResponse struct {
	Content string `json:"Content"`
}

// Дата в гриде: "20.09.2022 11:11" — формат не зависит от классов DevExpress.
var reGridDate = regexp.MustCompile(`\d{2}\.\d{2}\.\d{4}\s+\d{2}:\d{2}`)

// messageId ищем по всему ответу, а не только в HTML грида: у части
// разделов он приходит в параметрах команды, рядом с "Value".
// Нежадный .{0,60}? вместо [^0-9a-f] — потому что буквы a и e из слова
// "Value" сами являются hex-символами и обрывали прежний шаблон.
var reMessageID = regexp.MustCompile(`(?is)messageId.{0,60}?([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)

// FeedHead — верхушка ленты.
type FeedHead struct {
	Date string
	// MessageIDs — кандидаты в идентификатор верхнего сообщения, по порядку
	// правдоподобия. В ответе рядом со словом messageId встречается и шаблон
	// параметра команды, и реальный идентификатор строки, а какой из них
	// какой — по разметке не отличить. Поэтому пробуем по очереди: портал
	// сам подтвердит верный, вернув содержимое вместо ошибки.
	MessageIDs []string
}

// FeedLatest запрашивает список раздела и возвращает верхнее сообщение.
func (c *Client) FeedLatest(f Feed) (FeedHead, error) {
	body, err := json.Marshal(map[string]any{
		"Id":           f.PageID,
		"State":        0,
		"KbObjectType": f.KbObjectType,
		"Parameters":   []any{},
	})
	if err != nil {
		return FeedHead{}, err
	}

	req, err := c.newRequest(http.MethodPost, "/CommandManager/Execute", bytes.NewReader(body))
	if err != nil {
		return FeedHead{}, err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")

	resp, err := c.http.Do(req)
	if err != nil {
		return FeedHead{}, fmt.Errorf("лента %s: %w", f.Name, err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return FeedHead{}, fmt.Errorf("лента %s: статус %d", f.Name, resp.StatusCode)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return FeedHead{}, fmt.Errorf("лента %s: чтение ответа: %w", f.Name, err)
	}
	if isHTML(raw) {
		return FeedHead{}, fmt.Errorf("лента %s: получен HTML вместо JSON (сессия истекла)", f.Name)
	}

	var parsed commandResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return FeedHead{}, fmt.Errorf("лента %s: разбор ответа: %w", f.Name, err)
	}

	head := FeedHead{}
	// Дату берём из HTML грида — там она относится к верхней строке списка.
	head.Date = reGridDate.FindString(parsed.Content)
	// Кандидатов собираем сначала из Content (там реальные строки грида),
	// затем из всего ответа. Порядок важен: первым пробуется наиболее
	// вероятный.
	head.MessageIDs = collectMessageIDs(parsed.Content, string(raw))
	if head.Date == "" {
		return head, fmt.Errorf("лента %s: не удалось определить дату последнего сообщения", f.Name)
	}
	return head, nil
}

// collectMessageIDs достаёт уникальных кандидатов из нескольких источников.
func collectMessageIDs(parts ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range parts {
		for _, m := range reMessageID.FindAllStringSubmatch(p, -1) {
			if len(m) < 2 {
				continue
			}
			id := strings.ToLower(m[1])
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
			if len(out) >= 6 {
				return out
			}
		}
	}
	return out
}

// ParseFeedDate разбирает дату грида в московском времени.
func ParseFeedDate(s string) (time.Time, error) {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		loc = time.Local
	}
	return time.ParseInLocation("02.01.2006 15:04", s, loc)
}

// FeedNewer сообщает, свежее ли dateNew сохранённой dateOld.
// Пустая dateOld означает «раньше не забирали» — считаем новым.
func FeedNewer(dateNew, dateOld string) bool {
	if dateOld == "" {
		return true
	}
	tNew, err1 := ParseFeedDate(dateNew)
	tOld, err2 := ParseFeedDate(dateOld)
	if err1 != nil || err2 != nil {
		// Не разобрали — сравниваем строками, чтобы не пропустить обновление.
		return dateNew != dateOld
	}
	return tNew.After(tOld)
}

// Attachment — вложение сообщения. Имена полей сняты с ответа портала.
type Attachment struct {
	IDMessage string `json:"idMessage"`
	FileID    string `json:"idFile"`
	FileName  string `json:"fileName"`
}

// MessageContent — содержимое открытого сообщения.
type MessageContent struct {
	Subject     string
	Body        string
	SentDate    string
	Attachments []Attachment
}

// openResponse — фактическая структура ответа TextMessage/Open.
type openResponse struct {
	IsError bool   `json:"isError"`
	Message string `json:"message"`
	Data    struct {
		IDMessage     string       `json:"idMessage"`
		IDMessageType int          `json:"idMessageType"`
		Subject       string       `json:"subject"`
		Body          string       `json:"body"`
		SentDate      string       `json:"sentDate"`
		MessageFiles  []Attachment `json:"messageFiles"`
	} `json:"data"`
}

// OpenMessage открывает сообщение ленты.
func (c *Client) OpenMessage(f Feed, messageID string) (MessageContent, error) {
	body, err := json.Marshal(map[string]any{
		"messageId":     messageID,
		"IdMessageType": f.IdMessageType,
	})
	if err != nil {
		return MessageContent{}, err
	}

	req, err := c.newRequest(http.MethodPost, "/TextMessage/Open", bytes.NewReader(body))
	if err != nil {
		return MessageContent{}, err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")

	resp, err := c.http.Do(req)
	if err != nil {
		return MessageContent{}, fmt.Errorf("сообщение %s: %w", f.Name, err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return MessageContent{}, fmt.Errorf("сообщение %s: статус %d", f.Name, resp.StatusCode)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return MessageContent{}, fmt.Errorf("сообщение %s: чтение ответа: %w", f.Name, err)
	}

	var parsed openResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return MessageContent{}, fmt.Errorf("сообщение %s: разбор ответа: %w", f.Name, err)
	}
	if parsed.IsError {
		return MessageContent{}, fmt.Errorf("сообщение %s: портал вернул ошибку (IdMessageType=%d): %s",
			f.Name, f.IdMessageType, parsed.Message)
	}

	return MessageContent{
		Subject:     parsed.Data.Subject,
		Body:        parsed.Data.Body,
		SentDate:    parsed.Data.SentDate,
		Attachments: parsed.Data.MessageFiles,
	}, nil
}

// DownloadAttachment забирает один прикреплённый файл.
func (c *Client) DownloadAttachment(messageID, fileID string) ([]byte, string, error) {
	path := fmt.Sprintf("/TextMessage/GetFile?messageId=%s&fileId=%s",
		url.QueryEscape(messageID), url.QueryEscape(fileID))

	req, err := c.newRequest(http.MethodGet, path, nil)
	if err != nil {
		return nil, "", err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("вложение: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("вложение: статус %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("вложение: чтение ответа: %w", err)
	}
	if len(data) == 0 {
		return nil, "", fmt.Errorf("вложение: пустой ответ")
	}
	if isHTML(data) {
		return nil, "", fmt.Errorf("вложение: получен HTML вместо файла (сессия истекла)")
	}
	return data, extFromDisposition(resp.Header.Get("Content-Disposition")), nil
}

// FeedDisplayDate выбирает, какую дату показать: точную из сообщения
// или минутную из грида.
func FeedDisplayDate(gridDate, sentDate string) string {
	if strings.TrimSpace(sentDate) != "" {
		return sentDate
	}
	return gridDate
}
