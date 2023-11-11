package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gorilla/feeds"
	"github.com/microcosm-cc/bluemonday"
	"github.com/russross/blackfriday/v2"
	"go.uber.org/zap"
)

//go:embed embed
var embedded embed.FS

type BlogPost struct {
	Title   string
	Date    time.Time
	Slug    string
	Content template.HTML
}

const (
	timeFormat = time.DateOnly
	postSuffix = ".md"
)

func loadBlogPosts(logger *zap.Logger) ([]BlogPost, error) {
	var posts []BlogPost

	const prefix = "embed/writing"

	sub, err := fs.Sub(embedded, prefix)
	if err != nil {
		return nil, err
	}

	if err := fs.WalkDir(
		sub,
		".",
		func(fp string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			name := entry.Name()
			if !strings.HasSuffix(name, postSuffix) {
				return nil
			}

			name = strings.TrimSuffix(name, postSuffix)

			ts, err := time.Parse(timeFormat, name)
			if err != nil {
				logger.Warn(
					"improperly formatted writing entry, requires yyyy-mm-dd.md, skipping",
					zap.String("path", fp),
					zap.Error(err),
				)
				return nil
			}

			bp, err := parseBlogPost(filepath.Join(prefix, fp), ts)
			if err != nil {
				logger.Warn(
					"failed to parse blog post, skipping",
					zap.String("path", fp),
					zap.Error(err),
				)
				return nil
			}

			posts = append(posts, bp)

			return nil
		}); err != nil {
		return nil, err
	}

	// Sort posts from newest to oldest
	sort.Slice(posts, func(i, j int) bool {
		return posts[i].Date.After(posts[j].Date)
	})

	return posts, nil
}

func parseBlogPost(fp string, ts time.Time) (BlogPost, error) {
	var bp BlogPost
	bp.Date = ts

	f, err := embedded.Open(fp)
	if err != nil {
		return bp, err
	}
	defer f.Close()

	bcontent, err := io.ReadAll(f)
	if err != nil {
		return bp, err
	}
	contents := string(bcontent)

	if !strings.HasPrefix(contents, "# ") {
		return bp, errors.New("blog posts should start with a '#', i.e. a title")
	}
	contents = contents[2:]

	title, remaining, found := strings.Cut(contents, "\n")
	if !found {
		return bp, errors.New("titles should end with a newline character")
	}

	bp.Title = title
	bp.Slug = "/blog/" + ts.Format(timeFormat)

	unsafe := blackfriday.Run([]byte(strings.Trim(remaining, "\n\t ")))
	html := bluemonday.UGCPolicy().SanitizeBytes(unsafe)

	bp.Content = template.HTML(html)

	return bp, nil
}

func blogHandler(
	logger *zap.Logger,
	tset *templateSet,
	prefix string,
) (http.HandlerFunc, []BlogPost, error) {
	posts, err := loadBlogPosts(logger)
	if err != nil {
		return nil, nil, err
	}

	index := make(map[string]int, len(posts))
	for i, p := range posts {
		dfmt := p.Date.Format(timeFormat)
		if _, ok := index[dfmt]; ok {
			return nil, nil, fmt.Errorf(
				"overlapping blog post %s%s, shouldn't happen",
				dfmt,
				postSuffix,
			)
		}
		index[dfmt] = i
	}

	const (
		indexTmpl = "blog_index"
		postTmpl  = "blog_post"
	)

	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), "/")
		if path == "" {
			if !tset.has(indexTmpl) {
				logger.Warn("missing blog index template", zap.String("expected", indexTmpl))
				http.NotFound(w, r)
				return
			}

			var data struct {
				commonTemplateData
				Posts []BlogPost
			}
			data.commonTemplateData = loadCommonTemplateData(r)
			data.Posts = posts

			if err := tset.exec(w, indexTmpl, data); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}

			return
		}

		if !tset.has(postTmpl) {
			logger.Warn("missing blog post template", zap.String("expected", postTmpl))
			http.NotFound(w, r)
			return
		}

		postIdx, ok := index[path]
		if !ok {
			http.NotFound(w, r)
			return
		}

		var data struct {
			commonTemplateData
			Post BlogPost
		}
		data.commonTemplateData = loadCommonTemplateData(r)
		data.Post = posts[postIdx]

		if err := tset.exec(w, postTmpl, data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}, posts, nil
}

func rssFeedHandler(posts []BlogPost) http.HandlerFunc {
	feed := &feeds.Feed{
		Title:       "morgangallant.com blog",
		Link:        &feeds.Link{Href: "https://morgangallant.com/blog"},
		Description: "Rambling on the internet...",
		Author:      &feeds.Author{Name: "Morgan Gallant", Email: "morgan@morgangallant.com"},
		Created:     time.Now(),
	}
	policy := bluemonday.StripTagsPolicy()
	for _, p := range posts {
		feed.Items = append(feed.Items, &feeds.Item{
			Title:       p.Title,
			Link:        &feeds.Link{Href: "https://morgangallant.com" + p.Slug},
			Description: policy.Sanitize(string(p.Content[:min(len(p.Content), 100)])) + "...",
			Created:     p.Date,
		})
	}
	return func(w http.ResponseWriter, r *http.Request) {
		rss, err := feed.ToRss()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(rss))
	}
}

func staticHandler() (http.HandlerFunc, error) {
	sub, err := fs.Sub(embedded, "embed/static")
	if err != nil {
		return nil, err
	}
	fs := http.FileServer(http.FS(sub))
	return func(w http.ResponseWriter, r *http.Request) {
		fs.ServeHTTP(w, r)
	}, nil
}

type templateSet struct {
	tmpls map[string]*template.Template
}

const (
	tmplExt  = ".html"
	baseTmpl = "base"
)

func newTemplateSet(prefix string, fsys fs.FS) (*templateSet, error) {
	sub, err := fs.Sub(fsys, prefix)
	if err != nil {
		return nil, err
	}

	files, err := fs.ReadDir(sub, ".")
	if err != nil {
		return nil, err
	}

	const baseName = baseTmpl + tmplExt

	base := first(files, func(f fs.DirEntry) bool {
		return !f.IsDir() && f.Name() == baseName
	})
	if base == nil {
		return nil, fmt.Errorf("missing base template, expected to find %s", baseName)
	}

	tmpls := make(map[string]*template.Template)
	for _, f := range files {
		name := f.Name()
		if f.IsDir() || !strings.HasSuffix(name, tmplExt) || name == baseName {
			continue
		}
		id := strings.TrimSuffix(name, tmplExt)
		files := []string{
			filepath.Join(prefix, name),
			filepath.Join(prefix, (*base).Name()),
		}
		tmpl, err := template.New(id).ParseFS(
			fsys,
			files...,
		)
		if err != nil {
			return nil, err
		}
		tmpls[id] = tmpl
	}

	return &templateSet{tmpls}, nil
}

func (ts *templateSet) has(id string) bool {
	_, ok := ts.tmpls[id]
	return ok
}

func (ts *templateSet) exec(w io.Writer, id string, data any) error {
	t, ok := ts.tmpls[id]
	if !ok {
		return fmt.Errorf("missing template %s", id)
	}
	return t.ExecuteTemplate(w, baseTmpl, data)
}

func (ts *templateSet) handlerWithFallback(
	dataFunc func(r *http.Request) (any, error),
	fallback http.HandlerFunc,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index"
		}

		if !ts.has(path) {
			fallback(w, r)
			return
		}

		data, err := dataFunc(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err := ts.exec(w, path, data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

type commonTemplateData struct {
	ProcessingTime string
}

func loadCommonTemplateData(r *http.Request) (ctd commonTemplateData) {
	start, ok := r.Context().Value(initiatedAtCtxKey).(time.Time)
	if !ok {
		return
	}

	now := time.Now()

	// Note: Not entirely accurate, doesn't take into account time taken
	// to render the response (i.e. the html/template portion). Should
	// be negligible though...
	ctd.ProcessingTime = now.Sub(start).String()

	return ctd
}

type contextKey struct {
	name string
}

var initiatedAtCtxKey = &contextKey{name: "initiated-at"}

func initiatedAtMiddleware(inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), initiatedAtCtxKey, time.Now())
		inner.ServeHTTP(w, r.WithContext(ctx))
	})
}

func first[T any](arr []T, f func(T) bool) *T {
	for _, v := range arr {
		if f(v) {
			return &v
		}
	}
	return nil
}
