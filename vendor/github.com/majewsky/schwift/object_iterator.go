/*******************************************************************************
*
* Copyright 2018 Stefan Majewsky <majewsky@gmx.net>
*
* This program is free software: you can redistribute it and/or modify it under
* the terms of the GNU General Public License as published by the Free Software
* Foundation, either version 3 of the License, or (at your option) any later
* version.
*
* This program is distributed in the hope that it will be useful, but WITHOUT ANY
* WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS FOR
* A PARTICULAR PURPOSE. See the GNU General Public License for more details.
*
* You should have received a copy of the GNU General Public License along with
* this program. If not, see <http://www.gnu.org/licenses/>.
*
*******************************************************************************/

package schwift

import (
	"fmt"
	"time"
)

//ObjectInfo is a result type returned by ObjectIterator for detailed
//object listings. The metadata in this type is a subset of Object.Headers(),
//but since it is returned as part of the detailed object listing, it can be
//obtained without making additional HEAD requests on the object(s).
type ObjectInfo struct {
	Object       *Object
	SizeBytes    uint64
	ContentType  string
	Etag         string
	LastModified time.Time
}

//ObjectIterator iterates over the objects in a container. It is typically
//constructed with the Container.Objects() method. For example:
//
//	//either this...
//	iter := container.Objects()
//	iter.Prefix = "test-"
//	objects, err := iter.Collect()
//
//	//...or this
//	objects, err := schwift.ObjectIterator{
//		Container: container,
//		Prefix: "test-",
//	}.Collect()
//
//When listing objects via a GET request on the container, you can choose to
//receive object names only (via the methods without the "Detailed" suffix),
//or object names plus some basic metadata fields (via the methods with the
//"Detailed" suffix). See struct ObjectInfo for which metadata is returned.
//
//To obtain any other metadata, you can call Object.Headers() on the result
//object, but this will issue a separate HEAD request for each object.
//
//Use the "Detailed" methods only when you can use the extra metadata in struct
//ObjectInfo; detailed GET requests are more expensive than simple ones that
//return only object names.
type ObjectIterator struct {
	Container *Container
	//When Prefix is set, only objects whose name starts with this string are
	//returned.
	Prefix string
	//Options may contain additional headers and query parameters for the GET request.
	Options *RequestOptions

	//TODO: Delimiter field (and check if other stuff is missing)

	base *iteratorBase
}

func (i *ObjectIterator) getBase() *iteratorBase {
	if i.base == nil {
		i.base = &iteratorBase{i: i}
	}
	return i.base
}

//NextPage queries Swift for the next page of object names. If limit is
//>= 0, not more than that object names will be returned at once. Note
//that the server also has a limit for how many objects to list in one
//request; the lower limit wins.
//
//The end of the object listing is reached when an empty list is returned.
//
//This method offers maximal flexibility, but most users will prefer the
//simpler interfaces offered by Collect() and Foreach().
func (i *ObjectIterator) NextPage(limit int) ([]*Object, error) {
	names, err := i.getBase().nextPage(limit)
	if err != nil {
		return nil, err
	}

	result := make([]*Object, len(names))
	for idx, name := range names {
		result[idx] = i.Container.Object(name)
	}
	return result, nil
}

//NextPageDetailed is like NextPage, but includes basic metadata.
func (i *ObjectIterator) NextPageDetailed(limit int) ([]ObjectInfo, error) {
	b := i.getBase()

	var document []struct {
		SizeBytes       uint64 `json:"bytes"`
		ContentType     string `json:"content_type"`
		Etag            string `json:"hash"`
		LastModifiedStr string `json:"last_modified"`
		Name            string `json:"name"`
	}
	err := b.nextPageDetailed(limit, &document)
	if err != nil {
		return nil, err
	}
	if len(document) == 0 {
		b.setMarker("") //indicate EOF to iteratorBase
		return nil, nil
	}

	result := make([]ObjectInfo, len(document))
	for idx, data := range document {
		result[idx].Object = i.Container.Object(data.Name)
		result[idx].ContentType = data.ContentType
		result[idx].Etag = data.Etag
		result[idx].SizeBytes = data.SizeBytes
		result[idx].LastModified, err = time.Parse(time.RFC3339Nano, data.LastModifiedStr+"Z")
		if err != nil {
			//this error is sufficiently obscure that we don't need to expose a type for it
			return nil, fmt.Errorf("Bad field objects[%d].last_modified: %s", idx, err.Error())
		}
	}

	b.setMarker(result[len(result)-1].Object.Name())
	return result, nil
}

//Foreach lists the object names matching this iterator and calls the
//callback once for every object. Iteration is aborted when a GET request fails,
//or when the callback returns a non-nil error.
func (i *ObjectIterator) Foreach(callback func(*Object) error) error {
	for {
		objects, err := i.NextPage(-1)
		if err != nil {
			return err
		}
		if len(objects) == 0 {
			return nil //EOF
		}
		for _, o := range objects {
			err := callback(o)
			if err != nil {
				return err
			}
		}
	}
}

//ForeachDetailed is like Foreach, but includes basic metadata.
func (i *ObjectIterator) ForeachDetailed(callback func(ObjectInfo) error) error {
	for {
		infos, err := i.NextPageDetailed(-1)
		if err != nil {
			return err
		}
		if len(infos) == 0 {
			return nil //EOF
		}
		for _, ci := range infos {
			err := callback(ci)
			if err != nil {
				return err
			}
		}
	}
}

//Collect lists all object names matching this iterator. For large sets of
//objects that cannot be retrieved at once, Collect handles paging behind
//the scenes. The return value is always the complete set of objects.
func (i *ObjectIterator) Collect() ([]*Object, error) {
	var result []*Object
	for {
		objects, err := i.NextPage(-1)
		if err != nil {
			return nil, err
		}
		if len(objects) == 0 {
			return result, nil //EOF
		}
		result = append(result, objects...)
	}
}

//CollectDetailed is like Collect, but includes basic metadata.
func (i *ObjectIterator) CollectDetailed() ([]ObjectInfo, error) {
	var result []ObjectInfo
	for {
		infos, err := i.NextPageDetailed(-1)
		if err != nil {
			return nil, err
		}
		if len(infos) == 0 {
			return result, nil //EOF
		}
		result = append(result, infos...)
	}
}
