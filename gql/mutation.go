/*
 * Copyright (C) 2017 Dgraph Labs, Inc. and Contributors
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

package gql

import (
	"sort"
	"strconv"

	"github.com/dgraph-io/dgraph/protos"
	"github.com/dgraph-io/dgraph/types"
	"github.com/dgraph-io/dgraph/x"
	farm "github.com/dgryski/go-farm"
)

// Mutation stores the strings corresponding to set and delete operations.
type Mutation struct {
	Set    []*protos.NQuad
	Del    []*protos.NQuad
	Schema string
}

func (m Mutation) HasOps() bool {
	return len(m.Set) > 0 || len(m.Del) > 0 || len(m.Schema) > 0
}

func (m Mutation) NeededVars() []string {
	var vars []string
	addIfVar := func(name string) {
		if len(name) > 0 {
			vars = append(vars, name)
		}
	}
	for _, s := range m.Set {
		addIfVar(s.SubjectVar)
	}
	for _, d := range m.Del {
		addIfVar(d.SubjectVar)
	}
	sort.Strings(vars)
	return removeDuplicates(vars)
}

// Gets the uid corresponding to an xid from the posting list which stores the
// mapping.
func GetUid(xid string) (uint64, error) {
	// If string represents a UID, convert to uint64 and return.
	uid, err := strconv.ParseUint(xid, 0, 64)
	if err != nil {
		return farm.Fingerprint64([]byte(xid)), nil
	}
	if uid == 0 {
		return 0, x.Errorf("UID has to be greater than zero.")
	}
	return uid, nil
}

type NQuad struct {
	*protos.NQuad
}

func typeValFrom(val *protos.Value) types.Val {
	switch val.Val.(type) {
	case *protos.Value_BytesVal:
		return types.Val{types.BinaryID, val.GetBytesVal()}
	case *protos.Value_IntVal:
		return types.Val{types.IntID, val.GetIntVal()}
	case *protos.Value_StrVal:
		return types.Val{types.StringID, val.GetStrVal()}
	case *protos.Value_BoolVal:
		return types.Val{types.BoolID, val.GetBoolVal()}
	case *protos.Value_DoubleVal:
		return types.Val{types.FloatID, val.GetDoubleVal()}
	case *protos.Value_GeoVal:
		return types.Val{types.GeoID, val.GetGeoVal()}
	case *protos.Value_DateVal:
		return types.Val{types.DateID, val.GetDateVal()}
	case *protos.Value_DatetimeVal:
		return types.Val{types.DateTimeID, val.GetDatetimeVal()}
	case *protos.Value_PasswordVal:
		return types.Val{types.PasswordID, val.GetPasswordVal()}
	case *protos.Value_DefaultVal:
		return types.Val{types.DefaultID, val.GetDefaultVal()}
	}
	return types.Val{types.StringID, ""}
}

func byteVal(nq NQuad) ([]byte, error) {
	// We infer object type from type of value. We set appropriate type in parse
	// function or the Go client has already set.
	p := typeValFrom(nq.ObjectValue)
	// These three would have already been marshalled to bytes by the client or
	// in parse function.
	if p.Tid == types.GeoID || p.Tid == types.DateID || p.Tid == types.DateTimeID {
		return p.Value.([]byte), nil
	}

	p1 := types.ValueForType(types.BinaryID)
	if err := types.Marshal(p, &p1); err != nil {
		return []byte{}, err
	}
	return []byte(p1.Value.([]byte)), nil
}

// ToEdge is useful when you want to find the UID corresponding to XID for
// just one edge. The method doesn't automatically generate a UID for an XID.
func (nq NQuad) ToEdge() (*protos.DirectedEdge, error) {
	var err error
	sid, err := GetUid(nq.Subject)
	if err != nil {
		return nil, err
	}
	out := &protos.DirectedEdge{
		Attr:   nq.Predicate,
		Label:  nq.Label,
		Lang:   nq.Lang,
		Entity: sid,
		Facets: nq.Facets,
	}

	switch nq.valueType() {
	case x.ValueUid:
		oid, err := GetUid(nq.ObjectId)
		if err != nil {
			return nil, err
		}
		out.ValueId = oid
	case x.ValuePlain, x.ValueMulti:
		if err = copyValue(out, nq); err != nil {
			return &emptyEdge, err
		}
	}

	return out, nil
}

func toUid(xid string, newToUid map[string]uint64) (uid uint64, err error) {
	if id, present := newToUid[xid]; present {
		return id, err
	}
	return GetUid(xid)
}

var emptyEdge protos.DirectedEdge

func (nq NQuad) createEdge(subjectUid uint64, objectUid uint64) (*protos.DirectedEdge, error) {
	var err error
	out := &protos.DirectedEdge{
		Entity: subjectUid,
		Attr:   nq.Predicate,
		Label:  nq.Label,
		Lang:   nq.Lang,
		Facets: nq.Facets,
	}

	switch nq.valueType() {
	case x.ValueUid:
		out.ValueId = objectUid
	case x.ValuePlain, x.ValueMulti:
		if err = copyValue(out, nq); err != nil {
			return &emptyEdge, err
		}
	}
	return out, nil
}

// ToEdgeUsing determines the UIDs for the provided XIDs and populates the
// xidToUid map.
func (nq NQuad) ToEdgeUsing(newToUid map[string]uint64) (*protos.DirectedEdge, error) {
	var err error
	sUid, err := toUid(nq.Subject, newToUid)
	if err != nil {
		return nil, err
	}
	oUid, err := toUid(nq.ObjectId, newToUid)
	if err != nil {
		return nil, err
	}
	edge, err := nq.createEdge(sUid, oUid)
	if err != nil {
		return nil, err
	}
	return edge, nil
}

func (nq NQuad) ExpandSubjectVar(subjectUids []uint64, newToUid map[string]uint64) (edges []*protos.DirectedEdge, err error) {
	x.AssertTrue(len(nq.SubjectVar) > 0)

	objectUid, err := toUid(nq.Subject, newToUid)

	for _, uid := range subjectUids {
		e, err := nq.createEdge(uid, objectUid)
		if err != nil {
			return edges, err
		}
		edges = append(edges, e)
	}
	return
}

func copyValue(out *protos.DirectedEdge, nq NQuad) error {
	var err error
	if out.Value, err = byteVal(nq); err != nil {
		return err
	}
	out.ValueType = uint32(nq.ObjectType)
	return nil
}

func (nq NQuad) valueType() x.ValueTypeInfo {
	hasValue := nq.ObjectValue != nil
	hasLang := len(nq.Lang) > 0
	hasSpecialId := len(nq.ObjectId) == 0
	return x.ValueType(hasValue, hasLang, hasSpecialId)
}
