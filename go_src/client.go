package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

const (
	rootURL   = "https://portal.fedsfm.ru"
	userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
)

// Format описывает один скачиваемый формат перечня.
type Format struct {
	// Name — короткое имя формата, используется в именах файлов.
	Name string
	// QueryType — значение параметра type для /SkedDownload/GetActiveSked.
	QueryType string
}

// Форматы актуального перечня организаций и физических лиц,
// в отношении которых имеются сведения об их причастности
// к экстремистской деятельности или терроризму.
var (
	FormatXML  = Format{Name: "xml", QueryType: "xml21"}
	FormatWord = Format{Name: "doc", QueryType: "doc"}
)

// KnownFormats — форматы, доступные для скачивания по имени.
var KnownFormats = map[string]Format{
	"xml": FormatXML,
	"doc": FormatWord,
}

// Client — HTTP-клиент личного кабинета Росфинмониторинга.
type Client struct {
	http     *http.Client
	rootURL  string
	login    string
	password string
}

// NewClient создаёт клиента с собственным cookie jar.
// base пустой — используется рабочий адрес портала.
func NewClient(login, password, base string, timeout time.Duration) (*Client, error) {
	if base == "" {
		base = rootURL
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("cookie jar: %w", err)
	}
	return &Client{
		http: &http.Client{
			Jar:     jar,
			Timeout: timeout,
		},
		rootURL:  strings.TrimSuffix(base, "/"),
		login:    login,
		password: password,
	}, nil
}

func (c *Client) newRequest(method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, c.rootURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Origin", c.rootURL)
	req.Header.Set("Referer", c.rootURL+"/account/login")
	return req, nil
}

// Login выполняет вход в личный кабинет и сохраняет сессионные cookies.
//
// Портал отвечает 200 и на неудачный вход, поэтому после запроса сессия
// проверяется обращением к защищённому endpoint'у. Основной способ входа —
// JSON-тело (так делает сама форма кабинета); если он не сработал, пробуем
// устаревший вариант с параметрами в query string.
func (c *Client) Login() error {
	payload, err := json.Marshal(map[string]string{
		"Login":    c.login,
		"Password": c.password,
	})
	if err != nil {
		return err
	}

	if err := c.postLogin("/account/login", bytes.NewReader(payload)); err != nil {
		return err
	}
	if c.sessionIsValid() {
		return nil
	}

	form := url.Values{}
	form.Set("Login", c.login)
	form.Set("Password", c.password)
	if err := c.postLogin("/account/login?"+form.Encode(), nil); err != nil {
		return err
	}
	if c.sessionIsValid() {
		return nil
	}

	return errors.New("вход не выполнен: сессия не создана (проверьте логин и пароль)")
}

func (c *Client) postLogin(path string, body io.Reader) error {
	req, err := c.newRequest(http.MethodPost, path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("login request: %w", err)
	}
	defer drainAndClose(resp)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("login failed: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// sessionIsValid дёргает защищённый endpoint кабинета: анонимному клиенту
// портал отдаёт HTML страницы логина или редирект вместо JSON.
func (c *Client) sessionIsValid() bool {
	_, err := c.unreadNotifications()
	return err == nil
}

// Download скачивает архив с перечнем в указанном формате.
// Возвращает содержимое архива и расширение из заголовка Content-Disposition.
func (c *Client) Download(f Format) ([]byte, string, error) {
	req, err := c.newRequest(http.MethodGet, "/SkedDownload/GetActiveSked?type="+f.QueryType, nil)
	if err != nil {
		return nil, "", err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download %s: %w", f.Name, err)
	}
	defer drainAndClose(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download %s: unexpected status %d", f.Name, resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("download %s: read body: %w", f.Name, err)
	}
	if len(data) == 0 {
		return nil, "", fmt.Errorf("download %s: empty response", f.Name)
	}

	// Если сессия протухла, портал отдаёт HTML страницы логина вместо файла.
	if isHTML(data) {
		return nil, "", fmt.Errorf("download %s: получен HTML вместо файла (сессия истекла?)", f.Name)
	}

	return data, extFromDisposition(resp.Header.Get("Content-Disposition")), nil
}

// Notification — уведомление личного кабинета.
type notification struct {
	ID string `json:"idNotification"`
}

type notificationPayload struct {
	Data struct {
		Notifications []notification `json:"notifications"`
	} `json:"data"`
}

// MarkNotificationsRead помечает непрочитанные уведомления кабинета прочитанными.
// Ошибка здесь не критична для скачивания перечня.
func (c *Client) MarkNotificationsRead() (int, error) {
	ids, err := c.unreadNotifications()
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}

	payload, err := json.Marshal(ids)
	if err != nil {
		return 0, err
	}
	req, err := c.newRequest(http.MethodPost, "/EventNotifications/GetCheckedNotifications", bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer drainAndClose(resp)

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("mark notifications: unexpected status %d", resp.StatusCode)
	}
	return len(ids), nil
}

func (c *Client) unreadNotifications() ([]string, error) {
	body := strings.NewReader(`{"pageIndex": 1, "pageSize": 50, "isRead": false}`)
	req, err := c.newRequest(http.MethodPost, "/EventNotifications/GetNotifications", body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer drainAndClose(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get notifications: unexpected status %d", resp.StatusCode)
	}

	var data notificationPayload
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("get notifications: decode: %w", err)
	}

	ids := make([]string, 0, len(data.Data.Notifications))
	for _, n := range data.Data.Notifications {
		ids = append(ids, n.ID)
	}
	return ids, nil
}

func drainAndClose(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
}

func isHTML(data []byte) bool {
	head := bytes.ToLower(bytes.TrimSpace(data[:min(len(data), 512)]))
	return bytes.HasPrefix(head, []byte("<!doctype html")) || bytes.HasPrefix(head, []byte("<html"))
}

func extFromDisposition(cd string) string {
	if cd == "" {
		return ".zip"
	}
	_, params, err := mime.ParseMediaType(cd)
	if err == nil {
		if name := params["filename"]; name != "" {
			if i := strings.LastIndex(name, "."); i != -1 {
				return name[i:]
			}
		}
	}
	return ".zip"
}
