/******************************************************************************
*
*  Copyright 2018 Stefan Majewsky <majewsky@gmx.net>
*
*  Licensed under the Apache License, Version 2.0 (the "License");
*  you may not use this file except in compliance with the License.
*  You may obtain a copy of the License at
*
*      http://www.apache.org/licenses/LICENSE-2.0
*
*  Unless required by applicable law or agreed to in writing, software
*  distributed under the License is distributed on an "AS IS" BASIS,
*  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
*  See the License for the specific language governing permissions and
*  limitations under the License.
*
******************************************************************************/

package schwift

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"hash"
	"io"
	"net/http"
)

//Object represents a Swift object.
type Object struct {
	c    *Container
	name string
	//cache
	headers *ObjectHeaders
}

//Object returns a handle to the object with the given name within this
//container. This function does not issue any HTTP requests, and therefore cannot
//ensure that the object exists. Use the Exists() function to check for the
//container's existence.
func (c *Container) Object(name string) *Object {
	return &Object{c: c, name: name}
}

//Container returns a handle to the container this object is stored in.
func (o *Object) Container() *Container {
	return o.c
}

//Name returns the object name. This does not parse the name in any way; if you
//want only the basename portion of the object name, use package path in
//conjunction with this function. For example:
//
//	obj := account.Container("docs").Object("2018-02-10/invoice.pdf")
//	obj.Name()            //returns "2018-02-10/invoice.pdf"
//	path.Base(obj.Name()) //returns            "invoice.pdf"
func (o *Object) Name() string {
	return o.name
}

//FullName returns the container name and object name joined together with a
//slash. This identifier is used by Swift in several places (large object
//manifests, symlink targets, etc.) to refer to an object within an account.
//For example:
//
//	obj := account.Container("docs").Object("2018-02-10/invoice.pdf")
//	obj.Name()     //returns      "2018-02-10/invoice.pdf"
//	obj.FullName() //returns "docs/2018-02-10/invoice.pdf"
func (o *Object) FullName() string {
	return o.c.name + "/" + o.name
}

//Exists checks if this object exists, potentially by issuing a HEAD request
//if no Headers() have been cached yet.
func (o *Object) Exists() (bool, error) {
	_, err := o.Headers()
	if Is(err, http.StatusNotFound) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

//Headers returns the ObjectHeaders for this object. If the ObjectHeaders
//has not been cached yet, a HEAD request is issued on the object.
//
//This operation fails with http.StatusNotFound if the object does not exist.
func (o *Object) Headers() (ObjectHeaders, error) {
	if o.headers != nil {
		return *o.headers, nil
	}

	resp, err := Request{
		Method:        "HEAD",
		ContainerName: o.c.name,
		ObjectName:    o.name,
		//since Openstack LOVES to be inconsistent with everything (incl. itself),
		//this returns 200 instead of 204
		ExpectStatusCodes: []int{200},
	}.Do(o.c.a.backend)
	if err != nil {
		return ObjectHeaders{}, err
	}

	headers := ObjectHeaders{headersFromHTTP(resp.Header)}
	err = headers.Validate()
	if err != nil {
		return headers, err
	}
	o.headers = &headers
	return *o.headers, nil
}

//Update updates the object's headers using a POST request. To add URL
//parameters, pass a non-nil *RequestOptions.
//
//This operation returns http.StatusNotFound if the object does not exist.
//
//A successful POST request implies Invalidate() since it may change metadata.
func (o *Object) Update(headers ObjectHeaders, opts *RequestOptions) error {
	_, err := Request{
		Method:            "POST",
		ContainerName:     o.c.name,
		ObjectName:        o.name,
		Options:           cloneRequestOptions(opts, headers.Headers),
		ExpectStatusCodes: []int{202},
	}.Do(o.c.a.backend)
	if err == nil {
		o.Invalidate()
	}
	return err
}

//Upload creates the object using a PUT request.
//
//If you do not have an io.Reader, but you have a []byte or string instance
//containing the object, wrap it in a *bytes.Reader instance like so:
//
//	var buffer []byte
//	o.Upload(bytes.NewReader(buffer), opts)
//
//	//or...
//	var buffer string
//	o.Upload(bytes.NewReader([]byte(buffer)), opts)
//
//If you have neither an io.Reader nor a []byte or string, but you have a
//function that generates the object's content into an io.Writer, use
//UploadWithWriter instead.
//
//If content is a *bytes.Reader or a *bytes.Buffer instance, the Content-Length
//and Etag request headers will be computed automatically. Otherwise, it is
//highly recommended that the caller set these headers (if possible) to allow
//the server to check the integrity of the uploaded file.
//
//If Etag and/or Content-Length is supplied and the content does not match
//these parameters, http.StatusUnprocessableEntity is returned. If Etag is not
//supplied and cannot be computed in advance, Upload() will compute the Etag as
//data is read from the io.Reader, and compare the result to the Etag returned
//by Swift, returning ErrChecksumMismatch in case of mismatch. The object will
//have been uploaded at that point, so you will usually want to Delete() it.
//
//This function can be used regardless of whether the object exists or not.
//
//A successful PUT request implies Invalidate() since it may change metadata.
func (o *Object) Upload(content io.Reader, opts *RequestOptions) error {
	opts = cloneRequestOptions(opts, nil)
	hdr := ObjectHeaders{opts.Headers}
	tryComputeContentLength(content, hdr)
	tryComputeEtag(content, hdr)

	//could not compute Etag in advance -> need to check on the fly
	var hasher hash.Hash
	if !hdr.Etag().Exists() {
		hasher = md5.New()
		if content != nil {
			content = io.TeeReader(content, hasher)
		}
	}

	resp, err := Request{
		Method:            "PUT",
		ContainerName:     o.c.name,
		ObjectName:        o.name,
		Options:           opts,
		Body:              content,
		ExpectStatusCodes: []int{201},
		DrainResponseBody: true,
	}.Do(o.c.a.backend)
	if err != nil {
		return err
	}
	o.Invalidate()

	if hasher != nil {
		expectedEtag := hex.EncodeToString(hasher.Sum(nil))
		if expectedEtag != resp.Header.Get("Etag") {
			return ErrChecksumMismatch
		}
	}

	return nil
}

//TODO add support for strings.Reader below

func tryComputeContentLength(content io.Reader, headers ObjectHeaders) {
	h := headers.SizeBytes()
	if h.Exists() {
		return
	}
	switch r := content.(type) {
	case *bytes.Buffer:
		h.Set(uint64(r.Len()))
	case *bytes.Reader:
		h.Set(uint64(r.Len()))
	}
}

func tryComputeEtag(content io.Reader, headers ObjectHeaders) {
	h := headers.Etag()
	if h.Exists() {
		return
	}
	switch r := content.(type) {
	case *bytes.Buffer:
		//bytes.Buffer has a method that returns the unread portion of the buffer,
		//so this one is easy
		sum := md5.Sum(r.Bytes())
		h.Set(hex.EncodeToString(sum[:]))
	case *bytes.Reader:
		//bytes.Reader does not have such a method, but it is an io.Seeker, so we
		//can read the entire thing and then seek back to where we started
		hash := md5.New()
		n, _ := r.WriteTo(hash)
		r.Seek(-n, io.SeekCurrent)
		h.Set(hex.EncodeToString(hash.Sum(nil)))
	}
}

//UploadWithWriter is a variant of Upload that can be used when the object's
//content is generated by some function or package that takes an io.Writer
//instead of supplying an io.Reader. For example:
//
//	func greeting(target io.Writer, name string) error {
//	    _, err := fmt.Fprintf(target, "Hello %s!\n", name)
//	    return err
//	}
//
//	obj := container.Object("greeting-for-susan-and-jeffrey")
//	err := obj.UploadWithWriter(nil, func(w io.Writer) error {
//	    err := greeting(w, "Susan")
//	    if err == nil {
//	        err = greeting(w, "Jeffrey")
//	    }
//	    return err
//	})
//
//If you do not need an io.Writer, always use Upload instead.
func (o *Object) UploadWithWriter(opts *RequestOptions, callback func(io.Writer) error) error {
	reader, writer := io.Pipe()
	errChan := make(chan error)
	go func() {
		err := o.Upload(reader, opts)
		reader.CloseWithError(err) //stop the writer if it is still writing
		errChan <- err
	}()
	writer.CloseWithError(callback(writer)) //stop the reader if it is still reading
	return <-errChan
}

//Delete deletes the object using a DELETE request. To add URL parameters,
//pass a non-nil *RequestOptions.
//
//This operation fails with http.StatusNotFound if the object does not exist.
//
//A successful DELETE request implies Invalidate().
func (o *Object) Delete(opts *RequestOptions) error {
	_, err := Request{
		Method:            "DELETE",
		ContainerName:     o.c.name,
		ObjectName:        o.name,
		Options:           opts,
		ExpectStatusCodes: []int{204},
	}.Do(o.c.a.backend)
	if err == nil {
		o.c.Invalidate()
	}
	return err
}

//Invalidate clears the internal cache of this Object instance. The next call
//to Headers() on this instance will issue a HEAD request on the object.
func (o *Object) Invalidate() {
	o.headers = nil
}

//Download retrieves the object's contents using a GET request. This returns a
//helper object which allows you to select whether you want an io.ReadCloser
//for reading the object contents progressively, or whether you want the object
//contents collected into a byte slice or string.
//
//	reader, err := object.Download(nil, nil).AsReadCloser()
//
//	buf, err := object.Download(nil, nil).AsByteSlice()
//
//	str, err := object.Download(nil, nil).AsString()
//
//See struct DownloadedObject for details.
func (o *Object) Download(opts *RequestOptions) DownloadedObject {
	resp, err := Request{
		Method:            "GET",
		ContainerName:     o.c.name,
		ObjectName:        o.name,
		Options:           opts,
		ExpectStatusCodes: []int{200},
	}.Do(o.c.a.backend)
	var body io.ReadCloser
	if err == nil {
		newHeaders := ObjectHeaders{headersFromHTTP(resp.Header)}
		err = newHeaders.Validate()
		if err == nil {
			o.headers = &newHeaders
		}
		body = resp.Body
	}
	return DownloadedObject{body, err}
}

//CopyTo copies the object on the server side using a COPY request. To copy
//only the content, not the metadata, use the X-Fresh-Metadata header:
//
//	hdr := make(ObjectHeaders)
//	hdr.Set("X-Fresh-Metadata", "true")
//	err := sourceObject.CopyTo(targetObject, hdr, nil)
//
//If X-Fresh-Metadata is not set (or set to false), all metadata from the
//source object will be copied to the target, but you can overwrite metadata by
//providing new values in the headers argument, like with Update().
//
//A successful COPY implies target.Invalidate() since it may change the
//target's metadata.
func (o *Object) CopyTo(target *Object, opts *RequestOptions) error {
	opts = cloneRequestOptions(opts, nil)
	opts.Headers.Set("Destination", target.FullName())
	if o.c.a.name != target.c.a.name {
		opts.Headers.Set("Destination-Account", target.c.a.name)
	}

	_, err := Request{
		Method:            "COPY",
		ContainerName:     o.c.name,
		ObjectName:        o.name,
		Options:           opts,
		ExpectStatusCodes: []int{201},
		DrainResponseBody: true,
	}.Do(o.c.a.backend)
	if err == nil {
		target.Invalidate()
	}
	return err
}

//MoveTo moves the object on the server side, using a COPY request followed by
//a DELETE request on the source object.
//
//A successful move implies Invalidate() on both the source and target side.
func (o *Object) MoveTo(target *Object, copyOpts *RequestOptions, deleteOpts *RequestOptions) error {
	err := o.CopyTo(target, copyOpts)
	if err != nil {
		return err
	}
	return o.Delete(deleteOpts)
}
