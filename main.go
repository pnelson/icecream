package main

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/boltdb/bolt"
)

var (
	addr   = flag.String("addr", ":9000", "address to listen on")
	token  = flag.String("token", "", "slack API token")
	dbPath = flag.String("db-path", "icecream.db", "path to database file")
)

func init() {
	log.SetFlags(0)
}

func main() {
	flag.Parse()
	if *token == "" {
		log.Fatalln("token must be set")
	}
	db, err := bolt.Open(*dbPath, 0660, &bolt.Options{Timeout: 3 * time.Second})
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	s := &server{
		token: *token,
		store: &store{
			DB:         db,
			bucketName: []byte("icecream"),
		},
	}
	err = http.ListenAndServe(*addr, s)
	if err != nil {
		log.Fatal(err)
	}
}

type store struct {
	*bolt.DB
	bucketName []byte
}

func (db *store) add(name string) error {
	return db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(db.bucketName)
		if err != nil {
			return err
		}
		id, err := bucket.NextSequence()
		if err != nil {
			return err
		}
		return bucket.Put(itob(id), []byte(name))
	})
}

func (db *store) del(id uint64) (string, error) {
	var name []byte
	err := db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(db.bucketName)
		if err != nil {
			return err
		}
		key := itob(id)
		name = bucket.Get(key)
		return bucket.Delete(key)
	})
	return string(name), err
}

type user struct {
	id   uint64
	name string
}

func (db *store) list() ([]user, error) {
	var users []user
	err := db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(db.bucketName)
		if bucket == nil {
			return fmt.Errorf("bucket %q does not exist", db.bucketName)
		}
		c := bucket.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			u := user{
				id:   binary.BigEndian.Uint64(k),
				name: string(v),
			}
			users = append(users, u)
		}
		return nil
	})
	return users, err
}

type server struct {
	token string
	store *store
}

func (s *server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if isCertCheck(req) {
		return
	}
	if req.Method != http.MethodPost {
		abort(w, http.StatusMethodNotAllowed)
		return
	}
	if req.PostFormValue("token") != s.token {
		abort(w, http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(req.PostFormValue("text"))
	switch {
	case text == "help":
		s.help(w)
	case text == "list":
		s.list(w)
	case strings.HasPrefix(text, "add "):
		s.add(w, text[4:])
	case strings.HasPrefix(text, "del "):
		s.del(w, text[4:])
	}
}

func (s *server) help(w http.ResponseWriter) {
	lines := []string{
		"*Did someone leave their screen unlocked? Usage:*",
		"`/icecream add <username>` to add a user to the owing backlog",
		"`/icecream del <id>` to delete a user by id, use `list` to find id",
		"`/icecream list` to list owing users",
		"`/icecream help` to display this usage information",
	}
	text := strings.Join(lines, "\n")
	err := render(w, newPrivateMessage(text))
	if err != nil {
		abort(w, http.StatusInternalServerError)
		return
	}
}

func (s *server) list(w http.ResponseWriter) {
	users, err := s.store.list()
	if err != nil {
		abort(w, http.StatusInternalServerError)
		return
	}
	lines := make([]string, len(users))
	for i, u := range users {
		lines[i] = fmt.Sprintf("%d. %s", u.id, u.name)
	}
	text := strings.Join(lines, "\n")
	if text == "" {
		text = "The icecream backlog is empty. Tread lightly."
	}
	err = render(w, newPublicMessage(text))
	if err != nil {
		abort(w, http.StatusInternalServerError)
		return
	}
}

func (s *server) add(w http.ResponseWriter, name string) {
	err := s.store.add(name)
	if err != nil {
		abort(w, http.StatusInternalServerError)
		return
	}
	text := fmt.Sprintf("Added %s to the queue.", name)
	err = render(w, newPublicMessage(text))
	if err != nil {
		abort(w, http.StatusInternalServerError)
		return
	}
}

func (s *server) del(w http.ResponseWriter, id string) {
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		abort(w, http.StatusInternalServerError)
		return
	}
	name, err := s.store.del(n)
	if err != nil {
		abort(w, http.StatusInternalServerError)
		return
	}
	text := fmt.Sprintf("Deleted %s (%d) from the queue.", name, n)
	err = render(w, newPublicMessage(text))
	if err != nil {
		abort(w, http.StatusInternalServerError)
		return
	}
}

func abort(w http.ResponseWriter, code int) {
	http.Error(w, http.StatusText(code), code)
}

func isCertCheck(req *http.Request) bool {
	return req.Method == http.MethodGet && req.PostFormValue("ssl_check") == "1"
}

func itob(n uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, n)
	return b
}

type msg struct {
	Type string `json:"response_type"`
	Text string `json:"text"`
}

func newPublicMessage(text string) msg {
	return msg{text, "in_channel"}
}

func newPrivateMessage(text string) msg {
	return msg{text, "ephemeral"}
}

func render(w http.ResponseWriter, v msg) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, err = w.Write(b)
	return err
}
