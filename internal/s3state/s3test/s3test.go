// Package s3test is a stand-in for S3 to test against. It answers the three
// requests moonwalk makes (where a bucket is, what is in it, and an
// object's contents) the way S3 does, including the ways it refuses.
package s3test

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
)

// Bucket is a bucket the stand-in serves.
type Bucket struct {
	Region  string
	Objects map[string]string // By key.
	// Denied holds the keys that may not be read, and "" if the bucket may
	// not be listed.
	Denied []string
}

// Server is a running stand-in.
type Server struct {
	URL string
	// PageSize is how many keys a listing returns at once; S3's is 1000.
	PageSize int

	mu       sync.Mutex
	buckets  map[string]Bucket
	requests []string
}

// Start runs a stand-in serving the given buckets and points the AWS
// settings of the test's environment at it, with credentials for the
// given region and no configuration files.
func Start(t *testing.T, region string, buckets map[string]Bucket) *Server {
	t.Helper()
	s := &Server{PageSize: 1000, buckets: buckets}
	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	for name, value := range map[string]string{
		"AWS_ENDPOINT_URL_S3":         srv.URL,
		"AWS_ACCESS_KEY_ID":           "AKIAIOSFODNN7EXAMPLE",
		"AWS_SECRET_ACCESS_KEY":       "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"AWS_SESSION_TOKEN":           "",
		"AWS_REGION":                  region,
		"AWS_PROFILE":                 "",
		"AWS_CONFIG_FILE":             "/dev/null",
		"AWS_SHARED_CREDENTIALS_FILE": "/dev/null",
		"AWS_EC2_METADATA_DISABLED":   "true",
	} {
		t.Setenv(name, value)
	}
	return s
}

// Requests returns what was asked for so far, as "METHOD /path?query".
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// scope finds the region a request was signed for.
var scope = regexp.MustCompile(`Credential=[^/]+/\d{8}/([a-z0-9-]+)/s3/aws4_request`)

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.Method+" "+r.URL.RequestURI())
	s.mu.Unlock()

	name, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	bucket, ok := s.buckets[name]
	if !ok {
		fail(w, r, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist")
		return
	}
	// S3 names a bucket's region on every answer about it, refusals too.
	w.Header().Set("x-amz-bucket-region", bucket.Region)
	signed := scope.FindStringSubmatch(r.Header.Get("Authorization"))
	switch {
	case signed == nil:
		fail(w, r, http.StatusForbidden, "AccessDenied", "Access Denied")
		return
	case signed[1] != bucket.Region:
		fail(w, r, http.StatusMovedPermanently, "PermanentRedirect", "The bucket you are attempting to access must be addressed using the specified endpoint")
		return
	}
	denied := func(key string) bool {
		for _, d := range bucket.Denied {
			if d == key {
				return true
			}
		}
		return false
	}

	switch {
	case key == "" && r.Method == http.MethodHead:
		w.WriteHeader(http.StatusOK)
	case key == "" && r.URL.Query().Get("list-type") == "2":
		if denied("") {
			fail(w, r, http.StatusForbidden, "AccessDenied", "Access Denied")
			return
		}
		s.list(w, r, name, bucket)
	case key != "" && r.Method == http.MethodGet:
		body, ok := bucket.Objects[key]
		switch {
		case denied(key):
			fail(w, r, http.StatusForbidden, "AccessDenied", "Access Denied")
		case !ok:
			fail(w, r, http.StatusNotFound, "NoSuchKey", "The specified key does not exist.")
		default:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, body)
		}
	default:
		fail(w, r, http.StatusMethodNotAllowed, "MethodNotAllowed", "The specified method is not allowed against this resource.")
	}
}

// list answers ListObjectsV2, a page at a time.
func (s *Server) list(w http.ResponseWriter, r *http.Request, name string, bucket Bucket) {
	prefix, after := r.URL.Query().Get("prefix"), r.URL.Query().Get("continuation-token")
	var keys []string
	for key := range bucket.Objects {
		if strings.HasPrefix(key, prefix) && key > after {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	type object struct {
		Key  string
		Size int
	}
	result := struct {
		XMLName               xml.Name `xml:"http://s3.amazonaws.com/doc/2006-03-01/ ListBucketResult"`
		Name                  string
		Prefix                string
		KeyCount              int
		MaxKeys               int
		IsTruncated           bool
		NextContinuationToken string `xml:",omitempty"`
		Contents              []object
	}{Name: name, Prefix: prefix, MaxKeys: s.PageSize}
	if len(keys) > s.PageSize {
		keys = keys[:s.PageSize]
		result.IsTruncated = true
		result.NextContinuationToken = keys[len(keys)-1]
	}
	for _, key := range keys {
		result.Contents = append(result.Contents, object{key, len(bucket.Objects[key])})
	}
	result.KeyCount = len(keys)
	w.Header().Set("Content-Type", "application/xml")
	fmt.Fprint(w, xml.Header)
	xml.NewEncoder(w).Encode(result)
}

// fail answers with an error the way S3 does: a status and, unless the
// request was a HEAD, a body that names the error.
func fail(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		fmt.Fprintf(w, "%s<Error><Code>%s</Code><Message>%s</Message></Error>", xml.Header, code, message)
	}
}
