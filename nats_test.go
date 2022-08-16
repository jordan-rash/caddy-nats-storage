package certmagic_nats

import (
	"bytes"
	"context"
	"crypto/rand"
	"io/fs"
	"path"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/certmagic"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

var started bool
var startedLock sync.Mutex

func getTestData() (string, string, string, []string) {
	crt := path.Join("acme", "example.com", "sites", "example.com", "example.com.crt")
	key := path.Join("acme", "example.com", "sites", "example.com", "example.com.key")
	js := path.Join("acme", "example.com", "sites", "example.com", "example.com.json")
	want := []string{crt, key, js}
	sort.Strings(want)
	return crt, key, js, want
}

func startNatsServer() {
	startedLock.Lock()
	defer startedLock.Unlock()
	if started {
		return
	}

	opts := &server.Options{
		JetStream: true,
	}

	// Initialize new server with options
	ns, err := server.NewServer(opts)

	if err != nil {
		panic(err)
	}

	// Start the server via goroutine
	go ns.Start()
	// Wait for server to be ready for connections
	if !ns.ReadyForConnections(4 * time.Second) {
		panic("not ready for connection")
	}

	nc, err := nats.Connect(nats.DefaultURL)
	if err != nil {
		panic(err)
	}

	js, err := nc.JetStream()
	if err != nil {
		panic(err)
	}

	buckets := []string{"stat", "basic", "list"}
	for _, bucket := range buckets {
		_, err = js.CreateKeyValue(&nats.KeyValueConfig{
			Bucket:  bucket,
			Storage: nats.MemoryStorage,
		})
		if err != nil {
			panic(err)
		}
	}

	started = true
}

func getNatsClient(bucket string) *Nats {
	startNatsServer()

	n := &Nats{
		logger: zap.NewNop(),
		Hosts:  nats.DefaultURL,
		Bucket: bucket,
	}
	n.Provision(caddy.Context{})
	return n
}

func TestNats_Stat(t *testing.T) {
	n := getNatsClient("stat")

	data := make([]byte, 50)
	rand.Read(data)

	err := n.Store(context.Background(), "testStat1", data)
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	want := certmagic.KeyInfo{
		Key:        "testStat1",
		Size:       50,
		IsTerminal: true,
	}

	got, err := n.Stat(context.Background(), "testStat1")
	if err != nil {
		t.Errorf("Stat() error = %v", err)
	}

	if !reflect.DeepEqual(got.Modified, want) && time.Since(got.Modified) > time.Second {
		t.Errorf("Modified time too old: %v", got.Modified)
	}

	got.Modified = time.Time{}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Nats.Stat() = %v, want %v", got, want)
	}
}

func TestNats_StatKeyNotExists(t *testing.T) {
	n := getNatsClient("stat")
	got, err := n.Stat(context.Background(), "testStatNotExistingKey")
	if err != fs.ErrNotExist {
		t.Errorf("Stat() error = %v, want %v", err, fs.ErrNotExist)
	}

	if !reflect.DeepEqual(got, certmagic.KeyInfo{}) {
		t.Errorf("Stat() got = %v, want empty", got)
	}
}

func TestNats_StoreLoad(t *testing.T) {
	n := getNatsClient("basic")

	data := make([]byte, 50)
	rand.Read(data)

	err := n.Store(context.Background(), "test1", data)
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	got, err := n.Load(context.Background(), "test1")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("Load() got = %v, want %v", got, data)
	}
}

func TestNats_LoadKeyNotExists(t *testing.T) {
	n := getNatsClient("basic")

	got, err := n.Load(context.Background(), "NotExistingKey")
	if got != nil {
		t.Errorf("Load() got = %v, want nil", got)
	}

	if err != fs.ErrNotExist {
		t.Errorf("Load() error = %v, want %v", err, fs.ErrNotExist)
	}
}

func TestNats_Delete(t *testing.T) {
	n := getNatsClient("basic")

	err := n.Store(context.Background(), "testDelete", []byte("delete"))
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	err = n.Delete(context.Background(), "testDelete")
	if err != nil {
		t.Errorf("Delete() error = %v", err)
	}

	got, err := n.Load(context.Background(), "testDelete")
	if got != nil {
		t.Errorf("Load() got = %v, want nil", got)
	}

	if err != fs.ErrNotExist {
		t.Errorf("Load() error = %v, want %v", err, fs.ErrNotExist)
	}
}

func TestNats_List(t *testing.T) {
	n := getNatsClient("list")

	crt, key, js, want := getTestData()

	n.Store(context.Background(), crt, []byte("crt"))
	n.Store(context.Background(), key, []byte("key"))
	n.Store(context.Background(), js, []byte("meta"))

	matches := []string{path.Dir(crt), path.Dir(crt) + "/", ""}

	for _, v := range matches {
		keys, err := n.List(context.Background(), v, true)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		sort.Strings(keys)
		if !reflect.DeepEqual(keys, want) {
			t.Errorf("List() got = %v, want %v", keys, want)
		}
	}
}

func TestNats_ListNonRecursive(t *testing.T) {
	n := getNatsClient("list")

	crt, key, js, want := getTestData()

	n.Store(context.Background(), crt, []byte("crt"))
	n.Store(context.Background(), key, []byte("key"))
	n.Store(context.Background(), js, []byte("meta"))

	keys, err := n.List(context.TODO(), path.Join("acme", "example.com", "sites"), false)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(keys) > 0 {
		t.Errorf("List() got = %v, want empty", keys)
	}

	keys, err = n.List(context.TODO(), path.Join("acme", "example.com", "sites", "example.com"), false)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("List() got = %v, want %v", keys, want)
	}
}

func TestNats_Exists(t *testing.T) {
	n := getNatsClient("basic")

	n.Store(context.Background(), "testExists", []byte("exists"))

	got := n.Exists(context.Background(), "testExists")
	if !got {
		t.Errorf("Exists() got = %v, want true", got)
	}

	got = n.Exists(context.Background(), "testKeyNotExists")
	if got {
		t.Errorf("Exists() got = %v, want false", got)
	}
}

func TestNats_LockUnlock(t *testing.T) {
	n := getNatsClient("basic")
	lockKey := path.Join("acme", "example.com", "sites", "example.com")

	err := n.Lock(context.Background(), lockKey)
	if err != nil {
		t.Errorf("Unlock() error = %v", err)
	}

	err = n.Unlock(context.Background(), lockKey)
	if err != nil {
		t.Errorf("Unlock() error = %v", err)
	}
}

func TestNats_MultipleLocks(t *testing.T) {
	lockKey := path.Join("acme", "example.com", "sites", "example.com")

	n1 := getNatsClient("basic")
	n2 := getNatsClient("basic")
	n3 := getNatsClient("basic")

	err := n1.Lock(context.Background(), lockKey)
	if err != nil {
		t.Errorf("Lock() error = %v", err)
	}

	go func() {
		time.Sleep(200 * time.Millisecond)
		n1.Unlock(context.Background(), lockKey)
	}()

	err = n2.Lock(context.Background(), lockKey)
	if err != nil {
		t.Errorf("Lock() error = %v", err)
	}

	n2.Unlock(context.Background(), lockKey)

	time.Sleep(100 * time.Millisecond)
	err = n3.Lock(context.Background(), lockKey)
	if err != nil {
		t.Errorf("Lock() error = %v", err)
	}

	n3.Unlock(context.Background(), lockKey)
}

func FuzzNormalize(f *testing.F) {
	_, _, _, testcases := getTestData()
	for _, tc := range testcases {
		f.Add(tc) // Use f.Add to provide a seed corpus
	}
	f.Fuzz(func(t *testing.T, orig string) {
		if strings.Contains(orig, replaceChar) {
			return
		}

		rev := normalizeNatsKey(orig)
		doubleRev := denormalizeNatsKey(rev)
		if orig != doubleRev {
			t.Errorf("Before: %q, after: %q", orig, doubleRev)
		}
		if utf8.ValidString(orig) && !utf8.ValidString(rev) {
			t.Errorf("Reverse produced invalid UTF-8 string %q", rev)
		}
	})
}
