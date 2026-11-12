// Package s3state reads Terraform and OpenTofu states kept in S3, where the
// "s3" backend puts them.
//
// Credentials and the region come from where the AWS command line and
// Terraform take them: the environment, the shared configuration files and
// their profiles, single sign-on, an assumed role, or the instance or
// container the program runs on. A bucket in another region than the
// configured one is found. Another store that speaks the S3 protocol is
// reached by setting AWS_ENDPOINT_URL_S3.
//
// Nothing is written, and no lock is taken: reading a state needs only
// s3:GetObject on it, and listing a bucket s3:ListBucket.
package s3state

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Scheme starts the address of a state in S3.
const Scheme = "s3://"

// Location is a place in S3: one object, or everything under a prefix.
type Location struct {
	Bucket string
	// Key is an object's key, or a prefix if it is empty or ends in "/".
	Key string
}

// Parse reads an address of the form s3://bucket/key. ok is false if the
// address does not start with the scheme; an address that does, but names
// no bucket, is an error.
func Parse(address string) (loc Location, ok bool, err error) {
	rest, ok := strings.CutPrefix(address, Scheme)
	if !ok {
		return Location{}, false, nil
	}
	loc.Bucket, loc.Key, _ = strings.Cut(rest, "/")
	if loc.Bucket == "" {
		return Location{}, true, fmt.Errorf("%s names no bucket (want s3://bucket/key)", address)
	}
	return loc, true, nil
}

// IsPrefix reports whether the location is a prefix, not one object.
func (l Location) IsPrefix() bool {
	return l.Key == "" || strings.HasSuffix(l.Key, "/")
}

func (l Location) String() string {
	return Scheme + l.Bucket + "/" + l.Key
}

// Store reads from S3. It keeps one client per bucket, in the bucket's
// region, and is safe to use from several goroutines.
type Store struct {
	mu      sync.Mutex
	base    *aws.Config
	clients map[string]*s3.Client
}

// client returns the client for a bucket, finding the bucket's region the
// first time.
func (s *Store) client(ctx context.Context, bucket string) (*s3.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.clients[bucket]; ok {
		return c, nil
	}
	if s.base == nil {
		cfg, err := config.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, fmt.Errorf("s3: %w", err)
		}
		if cfg.Region == "" {
			// Any region will do to ask where a bucket is.
			cfg.Region = "us-east-1"
		}
		s.base = &cfg
	}
	// A store reached by its own address is not AWS: it has no regions to
	// find, and is asked for a bucket by path, which every such store
	// understands.
	custom := os.Getenv("AWS_ENDPOINT_URL_S3") != "" || os.Getenv("AWS_ENDPOINT_URL") != ""
	newClient := func(region string) *s3.Client {
		return s3.NewFromConfig(*s.base, func(o *s3.Options) {
			o.Region = region
			o.UsePathStyle = custom
			// An object stored without a checksum is still read; there
			// is nothing in that to tell the user about.
			o.DisableLogOutputChecksumValidationSkipped = true
		})
	}
	c := newClient(s.base.Region)
	if region := bucketRegion(ctx, c, bucket); region != "" && region != s.base.Region {
		c = newClient(region)
	}
	if s.clients == nil {
		s.clients = map[string]*s3.Client{}
	}
	s.clients[bucket] = c
	return c, nil
}

// bucketRegion asks S3 which region a bucket is in. S3 says so even when
// it refuses the request or redirects it, so the answer is taken from
// either. It is empty if there is none, in which case the configured
// region is used and the next request reports what is wrong.
func bucketRegion(ctx context.Context, c *s3.Client, bucket string) string {
	out, err := c.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	if err == nil {
		return aws.ToString(out.BucketRegion)
	}
	var re *awshttp.ResponseError
	if errors.As(err, &re) && re.Response != nil {
		return re.Response.Header.Get("x-amz-bucket-region")
	}
	return ""
}

// Keys returns the keys under the location's prefix that end in .tfstate,
// sorted.
func (s *Store) Keys(ctx context.Context, loc Location) ([]string, error) {
	c, err := s.client(ctx, loc.Bucket)
	if err != nil {
		return nil, err
	}
	var keys []string
	pages := s3.NewListObjectsV2Paginator(c, &s3.ListObjectsV2Input{Bucket: aws.String(loc.Bucket), Prefix: aws.String(loc.Key)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, explain(err, loc, "s3:ListBucket")
		}
		for _, object := range page.Contents {
			if key := aws.ToString(object.Key); strings.HasSuffix(key, ".tfstate") {
				keys = append(keys, key)
			}
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// Read returns the object at the location.
func (s *Store) Read(ctx context.Context, loc Location) ([]byte, error) {
	c, err := s.client(ctx, loc.Bucket)
	if err != nil {
		return nil, err
	}
	out, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(loc.Bucket), Key: aws.String(loc.Key)})
	if err != nil {
		return nil, explain(err, loc, "s3:GetObject")
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", loc, err)
	}
	return data, nil
}

// explain turns an error from S3 into one that says what was asked for
// and, where S3 gives a reason a person can act on, what to do about it.
func explain(err error, loc Location, permission string) error {
	var re *awshttp.ResponseError
	if errors.As(err, &re) {
		switch re.HTTPStatusCode() {
		case http.StatusNotFound:
			return fmt.Errorf("%s: not found", loc)
		case http.StatusForbidden:
			return fmt.Errorf("%s: access denied; the credentials in use need %s there (%w)", loc, permission, err)
		}
	}
	return fmt.Errorf("%s: %w", loc, err)
}
