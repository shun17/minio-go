/*
 * MinIO Go Library for Amazon S3 Compatible Cloud Storage
 * Copyright 2015-2020 MinIO, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package minio

import (
	"context"
	"fmt"
	"github.com/minio/minio-go/v7/pkg/s3utils"
)

// Bucket List Operations.
func (c *Client) listObjectsV2RateLimited(ctx context.Context, bucketName string, opts ListObjectsOptions) <-chan ObjectInfo {
	// Allocate new list objects channel.
	objectStatCh := make(chan ObjectInfo, 1)
	// Default listing is delimited at "/"
	delimiter := "/"
	if opts.Recursive {
		// If recursive we do not delimit.
		delimiter = ""
	}

	// Return object owner information by default
	fetchOwner := true

	sendObjectInfo := func(info ObjectInfo) {
		select {
		case objectStatCh <- info:
		case <-ctx.Done():
		}
	}

	// Validate bucket name.
	if err := s3utils.CheckValidBucketName(bucketName); err != nil {
		defer close(objectStatCh)
		sendObjectInfo(ObjectInfo{
			Err: err,
		})
		return objectStatCh
	}

	// Validate incoming object prefix.
	if err := s3utils.CheckValidObjectNamePrefix(opts.Prefix); err != nil {
		defer close(objectStatCh)
		sendObjectInfo(ObjectInfo{
			Err: err,
		})
		return objectStatCh
	}

	// Initiate list objects goroutine here.
	go func(objectStatCh chan<- ObjectInfo) {
		defer func() {
			if contextCanceled(ctx) {
				objectStatCh <- ObjectInfo{
					Err: ctx.Err(),
				}
			}
			close(objectStatCh)
		}()

		// Save continuationToken for next request.
		var continuationToken string
		for {
			// Get list of objects a maximum of 1000 per request.
			result, err := c.listObjectsV2Query(ctx, bucketName, opts.Prefix, continuationToken,
				fetchOwner, opts.WithMetadata, delimiter, opts.StartAfter, opts.MaxKeys, opts.headers)
			if err != nil {
				sendObjectInfo(ObjectInfo{
					Err: err,
				})
				return
			}

			// If contents are available loop through and send over channel.
			for _, object := range result.Contents {
				object.ETag = trimEtag(object.ETag)
				select {
				// Send object content.
				case objectStatCh <- object:
				// If receives done from the caller, return here.
				case <-ctx.Done():
					return
				}
			}

			// Send all common prefixes if any.
			// NOTE: prefixes are only present if the request is delimited.
			for _, obj := range result.CommonPrefixes {
				select {
				// Send object prefixes.
				case objectStatCh <- ObjectInfo{Key: obj.Prefix}:
				// If receives done from the caller, return here.
				case <-ctx.Done():
					return
				}
			}

			// If continuation token present, save it for next request.
			if result.NextContinuationToken != "" {
				continuationToken = result.NextContinuationToken
			}

			// Listing ends result is not truncated, return right here.
			if !result.IsTruncated {
				return
			}

			// Add this to catch broken S3 API implementations.
			if continuationToken == "" {
				sendObjectInfo(ObjectInfo{
					Err: fmt.Errorf("listObjectsV2 is truncated without continuationToken, %s S3 server is incompatible with S3 API", c.endpointURL),
				})
				return
			}
		}
	}(objectStatCh)
	return objectStatCh
}

// ListObjects returns objects list after evaluating the passed options.
//
//	api := client.New(....)
//	for object := range api.ListObjects(ctx, "mytestbucket", minio.ListObjectsOptions{Prefix: "starthere", Recursive:true}) {
//	    fmt.Println(object)
//	}
//
// If caller cancels the context, then the last entry on the 'chan ObjectInfo' will be the context.Error()
// caller must drain the channel entirely and wait until channel is closed before proceeding, without
// waiting on the channel to be closed completely you might leak goroutines.
func (c *Client) ListObjectsRateLimited(ctx context.Context, bucketName string, opts ListObjectsOptions) <-chan ObjectInfo {
	if opts.WithVersions {
		return c.listObjectVersions(ctx, bucketName, opts)
	}

	// Use legacy list objects v1 API
	if opts.UseV1 {
		return c.listObjects(ctx, bucketName, opts)
	}

	// Check whether this is snowball region, if yes ListObjectsV2 doesn't work, fallback to listObjectsV1.
	if location, ok := c.bucketLocCache.Get(bucketName); ok {
		if location == "snowball" {
			return c.listObjects(ctx, bucketName, opts)
		}
	}

	return c.listObjectsV2RateLimited(ctx, bucketName, opts)
}
