package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakePortal имитирует поведение личного кабинета: без валидной сессии
// защищённые endpoint'ы отдают HTML страницы логина, а не JSON/файл.
type fakePortal struct {
	login, password string
	loginCalls      []string
	authorized      bool
}

func (p *fakePortal) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/account/login", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ok := false
		if q := r.URL.Query(); q.Get("Login") != "" {
			p.loginCalls = append(p.loginCalls, "query")
			ok = q.Get("Login") == p.login && q.Get("Password") == p.password
		} else {
			p.loginCalls = append(p.loginCalls, "json")
			ok = strings.Contains(string(body), `"Login":"`+p.login+`"`) &&
				strings.Contains(string(body), `"Password":"`+p.password+`"`)
		}
		if ok {
			p.authorized = true
			http.SetCookie(w, &http.Cookie{Name: "ASPXAUTH", Value: "token", Path: "/"})
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	mux.HandleFunc("/EventNotifications/GetNotifications", func(w http.ResponseWriter, r *http.Request) {
		if !p.authorized {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<!DOCTYPE html><html>login page</html>"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"notifications":[{"idNotification":"a"},{"idNotification":"b"}]}}`))
	})

	mux.HandleFunc("/EventNotifications/GetCheckedNotifications", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	mux.HandleFunc("/SkedDownload/GetActiveSked", func(w http.ResponseWriter, r *http.Request) {
		if !p.authorized {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<!DOCTYPE html><html>login page</html>"))
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="`+r.URL.Query().Get("type")+`.zip"`)
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte{0x50, 0x4b, 0x03, 0x04, 'p', 'a', 'y'})
	})

	return mux
}

func newTestClient(t *testing.T, p *fakePortal, login, password string) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(p.handler())
	t.Cleanup(srv.Close)

	c, err := NewClient(login, password, srv.URL, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return c, srv
}

func TestLoginSuccess(t *testing.T) {
	p := &fakePortal{login: "user", password: "pass"}
	c, _ := newTestClient(t, p, "user", "pass")

	if err := c.Login(); err != nil {
		t.Fatalf("Login() = %v, want nil", err)
	}
	if len(p.loginCalls) != 1 || p.loginCalls[0] != "json" {
		t.Fatalf("ожидался один вход JSON-телом, got %v", p.loginCalls)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	p := &fakePortal{login: "user", password: "pass"}
	c, _ := newTestClient(t, p, "user", "wrong")

	err := c.Login()
	if err == nil {
		t.Fatal("Login() с неверным паролем должен возвращать ошибку")
	}
	if !strings.Contains(err.Error(), "проверьте логин и пароль") {
		t.Fatalf("непонятная ошибка: %v", err)
	}
	// Должны быть испробованы оба способа: JSON-тело и query string.
	if len(p.loginCalls) != 2 {
		t.Fatalf("ожидались две попытки входа, got %v", p.loginCalls)
	}
}

func TestLoginFallbackToQueryString(t *testing.T) {
	p := &fakePortal{login: "user", password: "pass"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Портал, принимающий только query string.
		if r.URL.Path == "/account/login" && r.URL.Query().Get("Login") == p.login {
			p.authorized = true
		}
		p.handler().ServeHTTP(w, r)
	}))
	defer srv.Close()

	c, err := NewClient("user", "pass", srv.URL, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	if err := c.Login(); err != nil {
		t.Fatalf("Login() = %v, want nil", err)
	}
}

func TestDownloadFormats(t *testing.T) {
	p := &fakePortal{login: "user", password: "pass"}
	c, _ := newTestClient(t, p, "user", "pass")
	if err := c.Login(); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		format Format
		want   string
	}{
		{FormatXML, "xml21.zip"},
		{FormatWord, "doc.zip"},
	} {
		data, ext, err := c.Download(tc.format)
		if err != nil {
			t.Fatalf("Download(%s) = %v", tc.format.Name, err)
		}
		if ext != ".zip" {
			t.Fatalf("ext = %q", ext)
		}
		if len(data) == 0 {
			t.Fatal("пустой ответ")
		}
	}
}

func TestDownloadRejectsLoginPage(t *testing.T) {
	p := &fakePortal{login: "user", password: "pass"}
	c, _ := newTestClient(t, p, "user", "pass")
	// Логин не выполняли — портал отдаст HTML вместо файла.

	if _, _, err := c.Download(FormatXML); err == nil {
		t.Fatal("HTML страницы логина не должен сохраняться как файл")
	}
}

func TestMarkNotificationsRead(t *testing.T) {
	p := &fakePortal{login: "user", password: "pass"}
	c, _ := newTestClient(t, p, "user", "pass")
	if err := c.Login(); err != nil {
		t.Fatal(err)
	}

	n, err := c.MarkNotificationsRead()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("отмечено %d уведомлений, want 2", n)
	}
}
