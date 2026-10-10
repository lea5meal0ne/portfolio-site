package main

import (
	"bytes"
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed web
var assets embed.FS

type Category struct {
	ID    string
	Name  string
	Order int
}
type Photo struct {
	ID       string
	Title    string
	Category string
	URL      string
	Thumb    string
	Order    int
	Demo     bool
}
type Settings struct {
	Name     string
	Intro    string
	About    string
	Email    string
	Telegram string
	Hero     string
}
type Database struct {
	Categories []Category
	Photos     []Photo
	Settings   Settings
}
type Password struct {
	Salt string
	Hash string
}
type Session struct {
	CSRF    string
	Expires time.Time
}
type Attempt struct {
	Count int
	Until time.Time
}
type App struct {
	mu        sync.RWMutex
	db        Database
	dir       string
	password  Password
	sessions  map[string]Session
	attempts  map[string]Attempt
	templates *template.Template
	origin    string
	secure    bool
}
type View struct {
	DB         Database
	Hero       Photo
	CSRF       string
	Error      string
	Notice     string
	Configured bool
	Year       int
}

func token() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func env(k, d string) string {
	if s := os.Getenv(k); s != "" {
		return s
	}
	return d
}
func seed() Database {
	cats := []Category{{"landscape", "Пейзажи", 0}, {"details", "Детали природы", 1}, {"travel", "Путешествия", 2}}
	names := []string{"Горный воздух", "Тишина леса", "Берег в тумане", "Линии горизонта", "Песок и свет", "Зеркало озера", "Течение"}
	files := []string{"mountains", "forest", "coast", "ridges", "dunes", "lake", "sea"}
	groups := []string{"landscape", "details", "travel", "landscape", "details", "landscape", "travel"}
	photos := []Photo{}
	for i, f := range files {
		photos = append(photos, Photo{ID: f, Title: names[i], Category: groups[i], URL: "/static/photos/" + f + ".jpg", Thumb: "/static/photos/" + f + "-thumb.jpg", Order: i, Demo: true})
	}
	return Database{Categories: cats, Photos: photos, Settings: Settings{Name: "Имя фотографа", Hero: "mountains"}}
}
func atomicJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".save-*")
	if e != nil {
		return e
	}
	temp := f.Name()
	defer os.Remove(temp)
	if e = f.Chmod(0600); e != nil {
		f.Close()
		return e
	}
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(temp, path)
}
func newApp(dir, origin string) (*App, error) {
	if e := os.MkdirAll(filepath.Join(dir, "uploads"), 0700); e != nil {
		return nil, e
	}
	if origin != "" {
		u, e := url.Parse(origin)
		if e != nil || u.Host == "" || u.Path != "" || (u.Scheme != "https" && u.Scheme != "http") {
			return nil, errors.New("PUBLIC_URL must be an origin, e.g. https://example.com")
		}
	}
	funcs := template.FuncMap{"category": func(db Database, id string) string {
		for _, c := range db.Categories {
			if c.ID == id {
				return c.Name
			}
		}
		return ""
	}, "count": func(db Database, id string) int {
		n := 0
		for _, p := range db.Photos {
			if id == "" || p.Category == id {
				n++
			}
		}
		return n
	}, "number": func(i int) string { return fmt.Sprintf("%02d", i+1) },
		"categoryCover": func(db Database, id string) Photo {
			for _, p := range db.Photos {
				if p.Category == id {
					return p
				}
			}
			return Photo{}
		},
		"coverSlides": func(v View) []Photo {
			slides := []Photo{}
			if v.Hero.ID != "" {
				slides = append(slides, v.Hero)
			}
			for _, p := range v.DB.Photos {
				if p.ID != v.Hero.ID {
					slides = append(slides, p)
				}
				if len(slides) >= 4 {
					break
				}
			}
			return slides
		},
	}
	ts, e := template.New("").Funcs(funcs).ParseFS(assets, "web/templates/*.html")
	if e != nil {
		return nil, e
	}
	a := &App{dir: dir, db: seed(), sessions: map[string]Session{}, attempts: map[string]Attempt{}, templates: ts, origin: origin, secure: strings.HasPrefix(origin, "https://")}
	b, e := os.ReadFile(filepath.Join(dir, "portfolio.json"))
	if e == nil {
		if e = json.Unmarshal(b, &a.db); e != nil {
			return nil, e
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	} else {
		if e = atomicJSON(filepath.Join(dir, "portfolio.json"), a.db); e != nil {
			return nil, e
		}
	}
	b, e = os.ReadFile(filepath.Join(dir, "password.json"))
	if e == nil {
		if e = json.Unmarshal(b, &a.password); e != nil {
			return nil, e
		}
		salt, se := hex.DecodeString(a.password.Salt)
		hash, he := hex.DecodeString(a.password.Hash)
		if se != nil || he != nil || len(salt) != 16 || len(hash) != 32 {
			return nil, errors.New("invalid password file")
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	return a, nil
}
func setPassword(dir, plain string) error {
	if len(plain) < 12 {
		return errors.New("пароль должен содержать минимум 12 символов")
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	salt := make([]byte, 16)
	if _, e := rand.Read(salt); e != nil {
		return e
	}
	hash, e := pbkdf2.Key(sha256.New, plain, salt, 600000, 32)
	if e != nil {
		return e
	}
	return atomicJSON(filepath.Join(dir, "password.json"), Password{Salt: hex.EncodeToString(salt), Hash: hex.EncodeToString(hash)})
}
func (a *App) snapshot() Database {
	a.mu.RLock()
	defer a.mu.RUnlock()
	b, _ := json.Marshal(a.db)
	var d Database
	json.Unmarshal(b, &d)
	sort.SliceStable(d.Photos, func(i, j int) bool { return d.Photos[i].Order < d.Photos[j].Order })
	sort.SliceStable(d.Categories, func(i, j int) bool { return d.Categories[i].Order < d.Categories[j].Order })
	return d
}
func (a *App) mutate(fn func(*Database) error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, _ := json.Marshal(a.db)
	var d Database
	json.Unmarshal(b, &d)
	if e := fn(&d); e != nil {
		return e
	}
	if e := atomicJSON(filepath.Join(a.dir, "portfolio.json"), d); e != nil {
		return e
	}
	a.db = d
	return nil
}
func (a *App) view() View {
	d := a.snapshot()
	v := View{DB: d, Configured: a.password.Hash != "", Year: time.Now().Year()}
	for _, p := range d.Photos {
		if p.ID == d.Settings.Hero {
			v.Hero = p
		}
	}
	if v.Hero.ID == "" && len(d.Photos) > 0 {
		v.Hero = d.Photos[0]
	}
	return v
}
func (a *App) render(w http.ResponseWriter, name string, v View, status int) {
	var b bytes.Buffer
	if e := a.templates.ExecuteTemplate(&b, name, v); e != nil {
		log.Print(e)
		http.Error(w, "Ошибка страницы", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write(b.Bytes())
}
func (a *App) session(r *http.Request) (string, Session, bool) {
	c, e := r.Cookie("portfolio_session")
	if e != nil {
		return "", Session{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[c.Value]
	if ok && time.Now().After(s.Expires) {
		delete(a.sessions, c.Value)
		ok = false
	}
	return c.Value, s, ok
}
func (a *App) sameOrigin(r *http.Request) bool {
	want := a.origin
	if want == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		want = scheme + "://" + r.Host
	}
	return r.Header.Get("Origin") == want
}
func (a *App) cookie(w http.ResponseWriter, name, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/studio", HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode, MaxAge: age})
}
func equal(x, y string) bool {
	return len(x) > 0 && len(x) == len(y) && subtle.ConstantTimeCompare([]byte(x), []byte(y)) == 1
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == "GET" {
		if _, _, ok := a.session(r); ok {
			http.Redirect(w, r, "/studio", 303)
			return
		}
		v := a.view()
		v.CSRF = token()
		a.cookie(w, "login_csrf", v.CSRF, 600)
		a.render(w, "login.html", v, 200)
		return
	}
	if r.Method != "POST" {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "Метод не разрешён", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if e := r.ParseForm(); e != nil {
		http.Error(w, "Некорректный запрос", 400)
		return
	}
	c, e := r.Cookie("login_csrf")
	if e != nil || !equal(c.Value, r.FormValue("csrf")) || !a.sameOrigin(r) {
		http.Error(w, "Запрос отклонён", 403)
		return
	}
	if a.password.Hash == "" {
		http.Error(w, "Вход ещё не настроен владельцем", 503)
		return
	}
	ip, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		ip = r.RemoteAddr
	}
	a.mu.Lock()
	now := time.Now()
	for k, v := range a.attempts {
		if now.After(v.Until) {
			delete(a.attempts, k)
		}
	}
	v := a.attempts[ip]
	if v.Count >= 5 && now.Before(v.Until) {
		a.mu.Unlock()
		w.Header().Set("Retry-After", "900")
		http.Error(w, "Слишком много попыток. Повторите через 15 минут.", 429)
		return
	}
	if len(a.attempts) >= 10000 {
		a.mu.Unlock()
		http.Error(w, "Повторите позже", 429)
		return
	}
	if v.Count == 0 {
		v.Until = now.Add(15 * time.Minute)
	}
	v.Count++
	a.attempts[ip] = v
	a.mu.Unlock()
	salt, _ := hex.DecodeString(a.password.Salt)
	want, _ := hex.DecodeString(a.password.Hash)
	hash, e := pbkdf2.Key(sha256.New, r.FormValue("password"), salt, 600000, 32)
	if e != nil || subtle.ConstantTimeCompare(hash, want) != 1 {
		view := a.view()
		view.Error = "Неверный пароль"
		view.CSRF = c.Value
		a.render(w, "login.html", view, 401)
		return
	}
	id, csrf := token(), token()
	a.mu.Lock()
	for k, s := range a.sessions {
		if now.After(s.Expires) {
			delete(a.sessions, k)
		}
	}
	if old, err := r.Cookie("portfolio_session"); err == nil {
		delete(a.sessions, old.Value)
	}
	a.sessions[id] = Session{csrf, now.Add(12 * time.Hour)}
	delete(a.attempts, ip)
	a.mu.Unlock()
	a.cookie(w, "portfolio_session", id, 12*3600)
	a.cookie(w, "login_csrf", "", -1)
	http.Redirect(w, r, "/studio", 303)
}
func bounded(s string, max int) bool { return len([]rune(strings.TrimSpace(s))) <= max }
func categoryExists(d *Database, id string) bool {
	for _, c := range d.Categories {
		if c.ID == id {
			return true
		}
	}
	return false
}
func (a *App) action(w http.ResponseWriter, r *http.Request, s Session) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if e := r.ParseMultipartForm(2 << 20); e != nil {
			http.Error(w, "Файлы слишком большие или повреждены (лимит запроса 64 МБ)", 400)
			return
		}
		defer r.MultipartForm.RemoveAll()
	} else {
		if e := r.ParseForm(); e != nil {
			http.Error(w, "Некорректные данные", 400)
			return
		}
	}
	if !equal(r.FormValue("csrf"), s.CSRF) || !a.sameOrigin(r) {
		http.Error(w, "Запрос отклонён", 403)
		return
	}
	action, id := r.FormValue("action"), r.FormValue("id")
	var err error
	switch action {
	case "logout":
		if t, _, ok := a.session(r); ok {
			a.mu.Lock()
			delete(a.sessions, t)
			a.mu.Unlock()
		}
		a.cookie(w, "portfolio_session", "", -1)
		http.Redirect(w, r, "/studio/login", 303)
		return
	case "category-add", "category-edit", "category-delete", "category-move":
		err = a.mutate(func(d *Database) error {
			name := strings.TrimSpace(r.FormValue("name"))
			if action == "category-add" || action == "category-edit" {
				if name == "" || !bounded(name, 60) {
					return errors.New("Название категории: от 1 до 60 символов")
				}
				for _, c := range d.Categories {
					if c.ID != id && strings.EqualFold(c.Name, name) {
						return errors.New("Категория с таким названием уже есть")
					}
				}
			}
			if action == "category-add" {
				d.Categories = append(d.Categories, Category{token()[:16], name, len(d.Categories)})
				return nil
			}
			for i, c := range d.Categories {
				if c.ID == id {
					switch action {
					case "category-edit":
						d.Categories[i].Name = name
					case "category-delete":
						for _, p := range d.Photos {
							if p.Category == id {
								return errors.New("Сначала перенесите или удалите фотографии из категории")
							}
						}
						d.Categories = append(d.Categories[:i], d.Categories[i+1:]...)
					case "category-move":
						sort.SliceStable(d.Categories, func(i, j int) bool { return d.Categories[i].Order < d.Categories[j].Order })
						for j, c := range d.Categories {
							if c.ID == id {
								target := j + 1
								if r.FormValue("direction") == "up" {
									target = j - 1
								}
								if target >= 0 && target < len(d.Categories) {
									d.Categories[j], d.Categories[target] = d.Categories[target], d.Categories[j]
								}
								break
							}
						}
					}
					for j := range d.Categories {
						d.Categories[j].Order = j
					}
					return nil
				}
			}
			return errors.New("Категория не найдена")
		})
	case "photo-edit", "photo-delete", "photo-move", "hero":
		err = a.mutate(func(d *Database) error {
			sort.SliceStable(d.Photos, func(i, j int) bool { return d.Photos[i].Order < d.Photos[j].Order })
			for i, p := range d.Photos {
				if p.ID == id {
					switch action {
					case "photo-edit":
						title := strings.TrimSpace(r.FormValue("title"))
						cat := r.FormValue("category")
						if title == "" || !bounded(title, 120) || !categoryExists(d, cat) {
							return errors.New("Проверьте название и категорию фотографии")
						}
						d.Photos[i].Title = title
						d.Photos[i].Category = cat
					case "photo-delete":
						d.Photos = append(d.Photos[:i], d.Photos[i+1:]...)
						if d.Settings.Hero == id {
							d.Settings.Hero = ""
						}
					case "hero":
						d.Settings.Hero = id
					case "photo-move":
						target := i + 1
						if r.FormValue("direction") == "up" {
							target = i - 1
						}
						if target >= 0 && target < len(d.Photos) {
							d.Photos[i], d.Photos[target] = d.Photos[target], d.Photos[i]
						}
					}
					for j := range d.Photos {
						d.Photos[j].Order = j
					}
					return nil
				}
			}
			return errors.New("Фотография не найдена")
		})
		if err == nil && action == "photo-delete" {
			for _, suffix := range []string{".jpg", "-thumb.jpg"} {
				if len(id) == 32 && !strings.ContainsAny(id, "/\\.") {
					os.Remove(filepath.Join(a.dir, "uploads", id+suffix))
				}
			}
		}
	case "settings":
		err = a.mutate(func(d *Database) error {
			n, intro, about := strings.TrimSpace(r.FormValue("name")), strings.TrimSpace(r.FormValue("intro")), strings.TrimSpace(r.FormValue("about"))
			email, tg := strings.TrimSpace(r.FormValue("email")), strings.TrimPrefix(strings.TrimSpace(r.FormValue("telegram")), "@")
			if n == "" || !bounded(n, 80) || !bounded(intro, 160) || !bounded(about, 3000) || !bounded(email, 254) {
				return errors.New("Проверьте длину текста")
			}
			if email != "" && (strings.ContainsAny(email, " \r\n<>") || strings.Count(email, "@") != 1) {
				return errors.New("Некорректная почта")
			}
			if len(tg) > 32 {
				return errors.New("Некорректное имя Telegram")
			}
			for _, c := range tg {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
					return errors.New("Укажите только имя Telegram без ссылки")
				}
			}
			d.Settings.Name = n
			d.Settings.Intro = intro
			d.Settings.About = about
			d.Settings.Email = email
			d.Settings.Telegram = tg
			return nil
		})
	case "upload":
		err = a.upload(r)
	default:
		err = errors.New("Неизвестное действие")
	}
	if err != nil {
		v := a.view()
		v.CSRF = s.CSRF
		v.Error = err.Error()
		a.render(w, "admin.html", v, 400)
		return
	}
	http.Redirect(w, r, "/studio?ok=1", 303)
}
func resize(src image.Image, max int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > max || h > max {
		if w >= h {
			h = h * max / w
			w = max
		} else {
			w = w * max / h
			h = max
		}
	}
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := src.At(b.Min.X+x*b.Dx()/w, b.Min.Y+y*b.Dy()/h)
			r, g, bb, aa := c.RGBA()
			dst.SetRGBA(x, y, color.RGBA{uint8((r + 0xffff - aa) >> 8), uint8((g + 0xffff - aa) >> 8), uint8((bb + 0xffff - aa) >> 8), 255})
		}
	}
	return dst
}
func (a *App) upload(r *http.Request) error {
	if r.MultipartForm == nil {
		return errors.New("Выберите фотографии")
	}
	fs := r.MultipartForm.File["photos"]
	if len(fs) == 0 || len(fs) > 12 {
		return errors.New("Можно загрузить от 1 до 12 фотографий за раз")
	}
	cat := r.FormValue("category")
	d := a.snapshot()
	if !categoryExists(&d, cat) {
		return errors.New("Выберите категорию")
	}
	staged := []Photo{}
	paths := []string{}
	committed := false
	defer func() {
		if !committed {
			for _, p := range paths {
				os.Remove(p)
			}
		}
	}()
	for _, f := range fs {
		if f.Size > 15<<20 {
			return errors.New("Каждый файл должен быть не больше 15 МБ")
		}
		h, e := f.Open()
		if e != nil {
			return e
		}
		b, e := io.ReadAll(io.LimitReader(h, (15<<20)+1))
		h.Close()
		if e != nil || len(b) > 15<<20 {
			return errors.New("Не удалось прочитать файл")
		}
		cfg, format, e := image.DecodeConfig(bytes.NewReader(b))
		if e != nil || (format != "jpeg" && format != "png") || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 30000000 {
			return errors.New("Допустимы JPEG и PNG до 30 мегапикселей")
		}
		img, _, e := image.Decode(bytes.NewReader(b))
		if e != nil {
			return errors.New("Повреждённое изображение")
		}
		id := token()[:32]
		for _, variant := range []struct {
			suffix string
			size   int
		}{{"", 2400}, {"-thumb", 720}} {
			p := filepath.Join(a.dir, "uploads", id+variant.suffix+".jpg")
			out, e := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if e != nil {
				return e
			}
			paths = append(paths, p)
			e = jpeg.Encode(out, resize(img, variant.size), &jpeg.Options{Quality: 88})
			closeErr := out.Close()
			if e != nil {
				return e
			}
			if closeErr != nil {
				return closeErr
			}
		}
		title := strings.TrimSuffix(filepath.Base(f.Filename), filepath.Ext(f.Filename))
		if !bounded(title, 120) {
			title = "Новая фотография"
		}
		if strings.TrimSpace(title) == "" {
			title = "Новая фотография"
		}
		staged = append(staged, Photo{ID: id, Title: title, Category: cat, URL: "/media/" + id + ".jpg", Thumb: "/media/" + id + "-thumb.jpg"})
	}
	e := a.mutate(func(d *Database) error {
		if !categoryExists(d, cat) {
			return errors.New("Категория уже удалена")
		}
		for _, p := range staged {
			p.Order = len(d.Photos)
			d.Photos = append(d.Photos, p)
		}
		return nil
	})
	if e == nil {
		committed = true
	}
	return e
}
func (a *App) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Метод не разрешён", 405)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		a.render(w, "index.html", a.view(), 200)
	})
	mux.Handle("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			http.Error(w, "Метод не разрешён", 405)
			return
		}
		p := "web" + r.URL.Path
		b, e := assets.ReadFile(p)
		if e != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeContent(w, r, filepath.Base(p), time.Time{}, bytes.NewReader(b))
	}))
	mux.HandleFunc("/media/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			http.Error(w, "Метод не разрешён", 405)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/media/")
		if strings.ContainsAny(name, "/\\") || !strings.HasSuffix(name, ".jpg") {
			http.NotFound(w, r)
			return
		}
		found := false
		for _, p := range a.snapshot().Photos {
			if p.URL == r.URL.Path || p.Thumb == r.URL.Path {
				found = true
				break
			}
		}
		if !found {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.ServeFile(w, r, filepath.Join(a.dir, "uploads", name))
	})
	mux.HandleFunc("/studio/login", a.login)
	mux.HandleFunc("/studio", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		_, s, ok := a.session(r)
		if !ok {
			http.Redirect(w, r, "/studio/login", 303)
			return
		}
		if r.Method != "GET" {
			http.Error(w, "Метод не разрешён", 405)
			return
		}
		v := a.view()
		v.CSRF = s.CSRF
		if r.URL.Query().Get("ok") == "1" {
			v.Notice = "Изменения сохранены"
		}
		a.render(w, "admin.html", v, 200)
	})
	mux.HandleFunc("/studio/action", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		_, s, ok := a.session(r)
		if !ok {
			http.Error(w, "Требуется вход", 401)
			return
		}
		if r.Method != "POST" {
			w.Header().Set("Allow", "POST")
			http.Error(w, "Метод не разрешён", 405)
			return
		}
		a.action(w, r, s)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		if a.secure {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		mux.ServeHTTP(w, r)
	})
}
func main() {
	if len(os.Args) == 3 && os.Args[1] == "export-preview" {
		dir, err := os.MkdirTemp("", "portfolio-preview-")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(dir)
		a, err := newApp(dir, "")
		if err != nil {
			log.Fatal(err)
		}
		out := os.Args[2]
		if err = os.MkdirAll(out, 0755); err != nil {
			log.Fatal(err)
		}
		if err = os.MkdirAll(filepath.Join(out, "static"), 0755); err != nil {
			log.Fatal(err)
		}
		var copyTree func(string) error
		copyTree = func(path string) error {
			entries, err := assets.ReadDir(path)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				p := path + "/" + entry.Name()
				target := filepath.Join(out, strings.TrimPrefix(p, "web/"))
				if entry.IsDir() {
					if err = os.MkdirAll(target, 0755); err != nil {
						return err
					}
					if err = copyTree(p); err != nil {
						return err
					}
				} else {
					b, err := assets.ReadFile(p)
					if err != nil {
						return err
					}
					if err = os.WriteFile(target, b, 0644); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if err = copyTree("web/static"); err != nil {
			log.Fatal(err)
		}
		f, err := os.Create(filepath.Join(out, "index.html"))
		if err != nil {
			log.Fatal(err)
		}
		if err = a.templates.ExecuteTemplate(f, "index.html", a.view()); err != nil {
			log.Fatal(err)
		}
		if err = f.Close(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("Static design preview exported; management requires the Go server.")
		return
	}
	dir := env("DATA_DIR", "data")
	if len(os.Args) > 1 && os.Args[1] == "set-password" {
		b, e := io.ReadAll(io.LimitReader(os.Stdin, 4096))
		if e != nil {
			log.Fatal(e)
		}
		if e = setPassword(dir, strings.TrimRight(string(b), "\r\n")); e != nil {
			log.Fatal(e)
		}
		fmt.Println("Пароль сохранён. Перезапустите сервер, если он уже работает.")
		return
	}
	a, e := newApp(dir, strings.TrimRight(os.Getenv("PUBLIC_URL"), "/"))
	if e != nil {
		log.Fatal(e)
	}
	addr := env("ADDR", "127.0.0.1:8080")
	if a.password.Hash == "" {
		log.Print("Пароль владельца не настроен. Панель закрыта до выполнения set-password.")
	}
	srv := &http.Server{Addr: addr, Handler: a.handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 90 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()
	log.Printf("Portfolio: http://%s", addr)
	if e = srv.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		log.Fatal(e)
	}
}
