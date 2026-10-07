package main

// Источники перечней в личном кабинете Росфинмониторинга.
//
// Каталоги отдаются обычными GET-запросами и возвращают готовый файл
// (zip/xml/doc), имя которого приходит в Content-Disposition.
//
// Ленты сообщений («Решения судов», «Информация Росфинмониторинга») здесь
// НЕ обрабатываются — они устроены иначе (грид DevExpress + вложения),
// см. feeds.go.

import (
	"fmt"
	"io"
	"net/http"
)

// Source — один скачиваемый источник.
type Source struct {
	// Name — короткое имя, попадает в имя сохраняемого файла.
	Name string
	// Section — подпапка внутри папки прогона.
	Section string
	// Title — человекочитаемое название (для письма и логов).
	Title string
	// Path — путь запроса на портале.
	Path string
}

// DefaultSources — все каталоги, которые скачиваются за один прогон.
var DefaultSources = []Source{
	{
		Name:    "xml",
		Section: "terrorism",
		Title:   "Перечень причастных к экстремистской деятельности или терроризму (XML 2.1)",
		Path:    "/SkedDownload/GetActiveSked?type=xml21",
	},
	{
		Name:    "doc",
		Section: "terrorism",
		Title:   "Перечень причастных к экстремистской деятельности или терроризму (Word)",
		Path:    "/SkedDownload/GetActiveSked?type=doc",
	},
	{
		Name:    "mvk",
		Section: "mvk",
		Title:   "Перечень лиц, в отношении которых действует решение МВК о замораживании (XML)",
		Path:    "/XmlCatalogDownload/GetActiveMvk",
	},
	{
		Name:    "oon-rus",
		Section: "oon",
		Title:   "Перечни Совета Безопасности ООН (XML, рус.)",
		Path:    "/XmlCatalogDownload/GetActiveOONRus",
	},
	{
		Name:    "oon-eng",
		Section: "oon",
		Title:   "Перечни Совета Безопасности ООН (XML, англ.)",
		Path:    "/XmlCatalogDownload/GetActiveOONEng",
	},
}

// SourceByName ищет источник по короткому имени.
func SourceByName(name string) (Source, bool) {
	for _, s := range DefaultSources {
		if s.Name == name {
			return s, true
		}
	}
	return Source{}, false
}

// SourceNames возвращает имена всех известных источников.
func SourceNames() []string {
	names := make([]string, len(DefaultSources))
	for i, s := range DefaultSources {
		names[i] = s.Name
	}
	return names
}

// AsFormat превращает источник в Format, чтобы переиспользовать
// существующую логику сохранения файлов из storage.go.
func (s Source) AsFormat() Format {
	return Format{Name: s.Name}
}

// DownloadSource скачивает один источник. Логика та же, что у Client.Download,
// но путь запроса берётся из самого источника, а не зашит в код.
func (c *Client) DownloadSource(s Source) ([]byte, string, error) {
	req, err := c.newRequest(http.MethodGet, s.Path, nil)
	if err != nil {
		return nil, "", fmt.Errorf("download %s: %w", s.Name, err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download %s: %w", s.Name, err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download %s: неожиданный статус %d", s.Name, resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("download %s: read body: %w", s.Name, err)
	}
	if len(data) == 0 {
		return nil, "", fmt.Errorf("download %s: empty response", s.Name)
	}
	// Если сессия протухла, портал отдаёт HTML страницы логина вместо файла.
	if isHTML(data) {
		return nil, "", fmt.Errorf("download %s: получен HTML вместо файла (сессия истекла)", s.Name)
	}

	return data, extFromDisposition(resp.Header.Get("Content-Disposition")), nil
}
