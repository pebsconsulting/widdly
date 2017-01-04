// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU General Public License as published by the Free
// Software Foundation, either version 3 of the License, or (at your option)
// any later version.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU General
// Public License for more details.
//
// You should have received a copy of the GNU General Public License along
// with this program.  If not, see <http://www.gnu.org/licenses/>.

// widdly is a self-hosted web application which can serve as a personal TiddlyWiki.
package main

import (
	"bytes"
	"compress/flate"
	"crypto/subtle"
	"flag"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/daaku/go.zipexe"
	"github.com/gorilla/securecookie"
	"github.com/kardianos/osext"

	"github.com/opennota/widdly/api"
	"github.com/opennota/widdly/store"
	_ "github.com/opennota/widdly/store/bolt"
)

var (
	addr       = flag.String("http", "127.0.0.1:8080", "HTTP service address")
	password   = flag.String("p", "", "Optional password to protect the wiki")
	dataSource = flag.String("db", "widdly.db", "Database file")

	hashKey      = securecookie.GenerateRandomKey(64)
	secureCookie = securecookie.New(hashKey, nil)
)

func main() {
	flag.Parse()

	// Open the data store and tell HTTP handlers to use it.
	api.Store = store.MustOpen(*dataSource)

	// Maybe read index.html from a zip archive appended to the current executable.
	wikiData := tryReadWikiFromExecutable()

	// Override api.ServeIndex to allow serving embedded index.html.
	wiki := pathToWiki()
	api.ServeIndex = func(w http.ResponseWriter, r *http.Request) {
		if fi, err := os.Stat(wiki); err == nil && isRegular(fi) { // Prefer the real file, if it exists.
			http.ServeFile(w, r, wiki)
		} else if len(wikiData) > 0 { // ...or use an embedded one.
			w.Header().Add("Content-Type", "text/html")
			w.Header().Add("Content-Encoding", "deflate")
			w.Header().Add("Content-Length", strconv.Itoa(len(wikiData)))
			w.Write(wikiData)
		} else {
			http.NotFound(w, r)
		}
	}

	// Optionally protect by a password.
	if *password != "" {
		// Set api.Authenticate and provide a login handler for simple password authentication.
		api.Authenticate = func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie("widdly_auth")
			if err != nil {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			var t time.Time
			if err = secureCookie.Decode("widdly_auth", c.Value, &t); err != nil {
				http.Redirect(w, r, "/login", http.StatusFound)
			}
		}
		http.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case "GET":
				w.Header().Add("Content-Type", "text/html")
				w.Write([]byte(`<html><body><form action="/login" method="POST">
							<input type="password" name="password" autofocus>
							<input type="submit" value="Let me in!">
							</form>
							</body>
							</html>`))
			case "POST":
				if subtle.ConstantTimeCompare([]byte(r.FormValue("password")), []byte(*password)) != 1 {
					http.Redirect(w, r, "/login", http.StatusFound)
				} else if encoded, err := secureCookie.Encode("widdly_auth", time.Now()); err == nil {
					http.SetCookie(w, &http.Cookie{
						Name:  "widdly_auth",
						Value: encoded,
						Path:  "/",
					})
					http.Redirect(w, r, "/", http.StatusFound)
				}
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
		})
	}

	log.Fatal(http.ListenAndServe(*addr, nil))
}

// pathToWiki returns a path that should be checked for index.html.
// If there is index.html, it should be put next to the executable.
// If for some reason pathToWiki fails to find the path to the current executable,
// it falls back to searching in the current directory.
func pathToWiki() string {
	dir := ""
	path, err := osext.Executable() //TODO(opennota): switch to the new os.Executable() once Go 1.8 is out.
	if err == nil {
		dir = filepath.Dir(path)
	} else if wd, err := os.Getwd(); err == nil {
		dir = wd
	}
	return filepath.Join(dir, "index.html")
}

// isRegular returns true iff the file described by fi is a regular file.
func isRegular(fi os.FileInfo) bool {
	return fi.Mode()&os.ModeType == 0
}

// deflate compresses data and returns the compressed slice.
func deflate(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, flate.BestCompression)
	_, err := w.Write(data)
	if err != nil {
		return nil, err
	}
	err = w.Close()
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// tryReadWikiFromExecutable tries to read index.html from a zip archive appended to the current executable.
// If it succeeds, it returns deflate-compressed index.html. If it fails, it returns nil.
func tryReadWikiFromExecutable() []byte {
	path, err := osext.Executable()
	if err != nil {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	r, err := zipexe.NewReader(f, fi.Size())
	if err != nil {
		return nil
	}
	for _, zf := range r.File {
		// Get the first .html file.
		if !strings.HasSuffix(zf.Name, ".html") {
			continue
		}

		// Read the archived index.html. Could use zf.DataOffset() and zf.CompressedSize64
		// and avoid the unnecessary decompression and compression steps,
		// but zipexe does not provide zip file offset relative to the executable.
		rc, err := zf.Open()
		if err != nil {
			return nil
		}
		defer rc.Close()
		data, err := ioutil.ReadAll(rc)
		if err != nil {
			return nil
		}
		compressed, err := deflate(data)
		if err != nil {
			return nil
		}
		return compressed
	}

	return nil
}