/*
 *  _
 * | | __ _ _ __  _   _  ___  _ __
 * | |/ _` | '_ \| | | |/ _ \| '_ \
 * | | (_| | | | | |_| | (_) | | | |
 * |_|\__,_|_| |_|\__, |\___/|_| |_|
 *                |___/
 *
 * markdown web server
 *
 * Author : Marcus Kazmierczak
 * Source : http://github.com/mkaz/lanyon
 * License: MIT
 *
 */

package main

import (
  "bytes"
  "encoding/json"
  "flag"
  "fmt"
  "io"
  "io/ioutil"
  "log"
  "net/http"
  "os"
  "os/exec"
  "path/filepath"
  "sort"
  "strings"
  "text/template"
  "time"
  "mime"
  "compress/gzip"

  "github.com/russross/blackfriday"
)

type gzipResponseWriter struct {
  io.Writer
  http.ResponseWriter
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
  return w.Writer.Write(b)
}

type expireDateConfig struct {
  Html       int
  Css        int
  Javascript int
  Image      int
  Index      int
}

// globals
var config struct {
  PortNum        int
  PublicDir      string
  TemplateDir    string
  RedirectDomain []string
  Less           []string
  ExpireTime     expireDateConfig
}

var configFile string

var defaultDays = 30

var ServerVersion = "0.3.0"

var ts *template.Template

type PagesSlice []Page

func (p PagesSlice) Get(i int) Page         { return p[i] }
func (p PagesSlice) Len() int               { return len(p) }
func (p PagesSlice) Less(i, j int) bool     { return p[i].Date.Unix() > p[j].Date.Unix() }
func (p PagesSlice) Swap(i, j int)          { p[i], p[j] = p[j], p[i] }
func (p PagesSlice) Sort()                  { sort.Sort(p) }
func (p PagesSlice) Limit(n int) PagesSlice { return p[0:n] }

// Log
type LanyonLog struct { }

func (ll LanyonLog) Text(text string) { log.Printf("%s ", text) }

func (ll LanyonLog) Request(req *http.Request) {
  text := []string{"->", req.Method, req.URL.RequestURI(), req.Proto}

  ll.Text(strings.Join(text, " "))
}

var Log = LanyonLog{}

// Page struct holds all data required for a web page
// Pages array is used for category pages with list of pages
type Page struct {
  Title, Content, Category, Layout, Url string
  Date                                  time.Time
  Pages                                 PagesSlice
  Params                                map[string]string
}

func startup() {
  loadConfig()

  log.Printf("Lanyon listening on http://localhost:%d", config.PortNum)

  // add trailing slashes to config directories
  if !strings.HasSuffix(config.PublicDir, "/") {
    config.PublicDir = config.PublicDir + "/"
  }

  // verify public directory exists
  if _, err := os.Stat(config.PublicDir); err != nil {
    log.Fatalln("Public directory does not exist")
  }

  // add trailing slashes to config directories
  if !strings.HasSuffix(config.TemplateDir, "/") {
    config.TemplateDir = config.TemplateDir + "/"
  }

  // verify template directory exists
  if _, err := os.Stat(config.TemplateDir); err != nil {
    log.Fatalln("Template directory does not exist")
  }

  if err := loadTemplates(); err != nil {
    log.Fatalln("Error Parsing Templates: ", err.Error())
  }

}

func loadTemplates() (err error) {
  ts, err = template.ParseGlob(config.TemplateDir + "*.html")
  return err
}

func main() {

  flag.StringVar(&configFile, "config", "lanyon.json", "specify a config file")
  flag.Parse()
  startup()

  http.HandleFunc("/", getRequest)

  colonport := fmt.Sprintf(":%d", config.PortNum)
  log.Fatal(http.ListenAndServe(colonport, nil))
}

// handler for all requests
func getRequest(w http.ResponseWriter, r *http.Request) {

  // check domain redirect returns true on redirect
  if domainRedirect(w, r) {
    return
  }

  // add default headers
  w.Header().Add("Server", "Lanyon " + ServerVersion)
  w.Header().Add("Vary", "Accept-Encoding")

  isIndex := false
  html := ""
  fullpath := filepath.Clean(config.PublicDir + r.URL.Path)
  ext := filepath.Ext(fullpath)
  modTime := time.Now()

  if ext == "" {
    // probaly, a directory list (index)
    ext = ".html"
    isIndex = true
  }

  setCacheExpirationDays(w, ext, isIndex)

  w.Header().Set("Content-Type", mime.TypeByExtension(ext))

  Log.Request(r)

  // check if file exists on filesystem
  if fi, err := os.Stat(fullpath); err == nil {
    if fi.IsDir() {
      modTime, html, err = getDirectoryListing(fullpath)
      if err != nil {
        showFourOhFour(w, r)
        return
      }
    } else {
      http.ServeFile(w, r, fullpath)

      return
    }
  } else { // file does not exist

    // check if false extension and an actual
    // file should be generated, or 404
    switch ext {
    case ".html":
      modTime, html, err = getMarkdownFile(fullpath)
      if err != nil {
        showFourOhFour(w, r)
        return
      }

    case ".css":
      modTime, html = getLessFile(fullpath)

    default:
      showFourOhFour(w, r)
      return
    }
  }

  w.Header().Set("Last-Modified", modTime.Format(time.RFC1123))

  if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
    fmt.Fprint(w, html)
    return
  }

  w.Header().Set("Content-Encoding", "gzip")

  gz := gzip.NewWriter(w)
  defer gz.Close()
  
  gzw := gzipResponseWriter{Writer: gz, ResponseWriter: w}

  gzw.Writer.Write( []byte(html) )
}

// directory listing checks for existing index file
// and if exists processes like any other markdown file
// otherwise gets directory listing of html and md files
// and creates a "category" page using the category.html
// template file with array of .Pages
func getDirectoryListing(dir string) (lastModified time.Time, html string, err error) {
  modTime := time.Now();

  // check for index.md
  indexfile := dir + "/index.md"
  if _, err := os.Stat(indexfile); err == nil {
    return getMarkdownFile(indexfile)
  }

  page := Page{}
  page.Title = filepath.Base(dir)
  page.Layout = "category"
  page.Category = filepath.Base(dir)

  var files []string
  dirlist, _ := ioutil.ReadDir(dir)
  for _, fi := range dirlist {
    f := filepath.Join(dir, fi.Name())
    ext := filepath.Ext(f)
    if ext == ".html" || ext == ".md" {
      files = append(files, f)
    }
  }

  // read markdown files to get title, date
  for fileIndex, f := range files {
    pg := readParseFile(f)
    filename := strings.Replace(f, ".md", ".html", 1)
    pg.Url = "/" + strings.Replace(filename, config.PublicDir, "", 1)
    page.Pages = append(page.Pages, pg)

    if fileIndex == 0 {
      modTime = pg.Date
    }
  }

  page.Pages.Sort()
  html = applyTemplates(page)

  return modTime, html, err
}

// reads markdown file, parse front matter and render content
// using template file defined by layout: param or by default
// uses post.html template
func getMarkdownFile(fullpath string) (lastModified time.Time, html string, err error) {
  mdfile := strings.Replace(fullpath, ".html", ".md", 1)
  page := Page{}

  statinfo, err := os.Stat(mdfile)

  if err == nil {
    page = readParseFile(mdfile)
  } else {
    return time.Now(), "", fmt.Errorf("Error reading file %s ", mdfile)
  }

  html = applyTemplates(page)
  return statinfo.ModTime(), html, err
}

func getLessFile(fullpath string) (time.Time, string) {
  lessfile := strings.Replace(fullpath, ".css", ".less", 1)

  path, err := exec.LookPath("lessc")

  if err != nil {
    return time.Now(), "/* Less is not installed */"
  }

  // requires lessc binary
  cmd := exec.Command(path, strings.Join(config.Less, " "), lessfile)
  output, err := cmd.Output()

  if err != nil {
    log.Println("Less error. Trying without parameters. [Try to update your lessc version]")

    // try without parameters
    cmd = exec.Command(path, lessfile)
    output, err = cmd.Output()

    if err != nil {
      return time.Now(), "/* Less error */"
    }
  }

  if statinfo, err := os.Stat(lessfile); err != nil {
    return time.Now(), "/* Error getting statinfo */"
  } else {
    return statinfo.ModTime(), string(output)
  }

}

func showFourOhFour(w http.ResponseWriter, r *http.Request) {
  md404 := config.PublicDir + "404.md"

  _, html, err := getMarkdownFile(md404)
  if err != nil {
    http.NotFound(w, r)
    return
  }

  w.WriteHeader(http.StatusNotFound)
  fmt.Fprint(w, html)
}

// checks config.RedirectDomain for ["domain1", "domain2"]
// If config is set, checks if request domain matches domain1
// if request does not match, issues 301 redirect to "domain2"
// Used to handle non "www.mkaz.com" requests to redirect
// to www.mkaz.com
// @return bool - true if redirected, false otherwise
//
func domainRedirect(w http.ResponseWriter, r *http.Request) bool {
  if len(config.RedirectDomain) != 2 {
    return false
  }

  if r.Host == config.RedirectDomain[0] {
    return false
  }
  redirect_url := fmt.Sprintf("http://%s/%s", config.RedirectDomain[1], r.RequestURI)
  http.Redirect(w, r, redirect_url, 301)
  return true
}

// read and parse markdown filename
func readParseFile(filename string) (page Page) {

  // setup default page struct
  page = Page{
    Title:    "",
    Content:  "",
    Category: getDirName(filename),
    Layout:   "post",
    Date:     time.Now(),
    Params:   make(map[string]string),
  }

  var data, err = ioutil.ReadFile(filename)
  if err != nil {
    return
  }

  // parse front matter from --- to ---
  var lines = strings.Split(string(data), "\n")
  var found = 0
  for i, line := range lines {
    line = strings.TrimSpace(line)

    if found == 1 {
      // parse line for param
      colonIndex := strings.Index(line, ":")
      if colonIndex > 0 {
        key := strings.TrimSpace(line[:colonIndex])
        value := strings.TrimSpace(line[colonIndex+1:])
        value = strings.Trim(value, "\"") //remove quotes
        switch key {
        case "title":
          page.Title = value
        case "layout":
          page.Layout = value
        case "date":
          page.Date, _ = time.Parse("2006-01-02", value)
        default:
          page.Params[key] = value
        }
      }
    } else if found >= 2 {
      // params over
      lines = lines[i:]
      break
    }

    if line == "---" {
      found += 1
    }
  }

  // convert markdown content
  content := strings.Join(lines, "\n")
  output := markdownRender([]byte(content))
  page.Content = string(output)

  return page
}

func applyTemplates(page Page) string {
  buffer := new(bytes.Buffer)
  templateFile := ""
  if page.Layout == "" {
    templateFile = "post.html"
  } else {
    templateFile = page.Layout + ".html"
  }

  ts.ExecuteTemplate(buffer, templateFile, page)
  return buffer.String()
}

// configure markdown render options
// See blackfriday markdown source for details
func markdownRender(content []byte) []byte {
  htmlFlags := 0
  //htmlFlags |= blackfriday.HTML_SKIP_SCRIPT
  htmlFlags |= blackfriday.HTML_USE_XHTML
  htmlFlags |= blackfriday.HTML_USE_SMARTYPANTS
  htmlFlags |= blackfriday.HTML_SMARTYPANTS_FRACTIONS
  htmlFlags |= blackfriday.HTML_SMARTYPANTS_LATEX_DASHES
  renderer := blackfriday.HtmlRenderer(htmlFlags, "", "")

  extensions := 0
  extensions |= blackfriday.EXTENSION_NO_INTRA_EMPHASIS
  extensions |= blackfriday.EXTENSION_TABLES
  extensions |= blackfriday.EXTENSION_FENCED_CODE
  extensions |= blackfriday.EXTENSION_AUTOLINK
  extensions |= blackfriday.EXTENSION_STRIKETHROUGH
  extensions |= blackfriday.EXTENSION_SPACE_HEADERS

  return blackfriday.Markdown(content, renderer, extensions)
}

func loadConfig() {

  // checks if specified file exists, either flag passed in
  // or default lanyon.json in current directory
  if _, err := os.Stat(configFile); os.IsNotExist(err) {

    // config file not found
    // lets check one more spot in /etc/
    configFile = "/etc/lanyon.json"
    if _, err := os.Stat(configFile); os.IsNotExist(err) {
      log.Fatalln("Config file not found, /etc/lanyon.json or specify with --config=FILENAME")
    }
  }

  file, err := ioutil.ReadFile(configFile)
  if err != nil {
    log.Fatalln("Error reading config file:", err)
  }

  if err := json.Unmarshal(file, &config); err != nil {
    log.Fatalln("Error parsing config lanyon.json: ", err)
  }
}

// returns directory name from filepath
func getDirName(fullpath string) (dir string) {
  dir = filepath.Dir(fullpath)
  dir = filepath.Base(dir)
  if strings.Trim(dir, "/") == strings.Trim(config.PublicDir, "/") {
    dir = ""
  }
  return
}

func setCacheExpirationDays(w http.ResponseWriter, ext string, isIndex bool) {
  var days int

  if isIndex == true {
    days = config.ExpireTime.Index
  } else {
    switch mime.TypeByExtension(ext) {
    case "text/html":
      days = config.ExpireTime.Html
    case "text/css":
      days = config.ExpireTime.Css
    case "application/javascript":
      days = config.ExpireTime.Javascript
    case "image/jpeg", "image/gif", "image/webm":
      days = config.ExpireTime.Image
    default:
      days = defaultDays
    }
  }

  if days == 0 {
    days = defaultDays
  }

  d := time.Duration(int64(time.Hour) * 24 * int64(days))
  expireDate := time.Now().Add(d)
  expireSecs := days * 24 * 60 * 60
  w.Header().Add("Cache-Control", fmt.Sprintf("max-age=%d, public", expireSecs))
  w.Header().Add("Expires", expireDate.Format(time.RFC1123))
}

