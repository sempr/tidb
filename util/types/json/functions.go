// Copyright 2017 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package json

import (
	"bytes"
	"fmt"
	"unicode/utf8"

	"github.com/juju/errors"
)

// Type returns type of JSON as string.
func (j JSON) Type() string {
	switch j.typeCode {
	case typeCodeObject:
		return "OBJECT"
	case typeCodeArray:
		return "ARRAY"
	case typeCodeLiteral:
		switch byte(j.i64) {
		case jsonLiteralNil:
			return "NULL"
		default:
			return "BOOLEAN"
		}
	case typeCodeInt64:
		return "INTEGER"
	case typeCodeFloat64:
		return "DOUBLE"
	case typeCodeString:
		return "STRING"
	default:
		msg := fmt.Sprintf(unknownTypeCodeErrorMsg, j.typeCode)
		panic(msg)
	}
}

// Extract receives several path expressions as arguments, matches them in j, and returns:
//  ret: target JSON matched any path expressions. maybe autowrapped as an array.
//  found: true if any path expressions matched.
func (j JSON) Extract(pathExprList []PathExpression) (ret JSON, found bool) {
	elemList := make([]JSON, 0, len(pathExprList))
	for _, pathExpr := range pathExprList {
		elemList = append(elemList, extract(j, pathExpr)...)
	}
	if len(elemList) == 0 {
		found = false
	} else if len(pathExprList) == 1 && len(elemList) == 1 {
		// If pathExpr contains asterisks, len(elemList) won't be 1
		// even if len(pathExprList) equals to 1.
		found = true
		ret = elemList[0]
	} else {
		found = true
		ret.typeCode = typeCodeArray
		ret.array = append(ret.array, elemList...)
	}
	return
}

// Unquote is for JSON_UNQUOTE.
func (j JSON) Unquote() (string, error) {
	switch j.typeCode {
	case typeCodeString:
		return unquoteString(j.str)
	default:
		return j.String(), nil
	}
}

// unquoteString recognizes the escape sequences shown in:
// https://dev.mysql.com/doc/refman/5.7/en/json-modification-functions.html#json-unquote-character-escape-sequences
func unquoteString(s string) (string, error) {
	ret := new(bytes.Buffer)
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			if i == len(s) {
				return "", errors.New("Missing a closing quotation mark in string")
			}
			switch s[i] {
			case '"':
				ret.WriteByte('"')
			case 'b':
				ret.WriteByte('\b')
			case 'f':
				ret.WriteByte('\f')
			case 'n':
				ret.WriteByte('\n')
			case 'r':
				ret.WriteByte('\r')
			case 't':
				ret.WriteByte('\t')
			case '\\':
				ret.WriteByte('\\')
			case 'u':
				if i+4 >= len(s) {
					return "", errors.New("Invalid unicode")
				}
				unicode, size := utf8.DecodeRuneInString(s[i-1 : i+5])
				utf8Buf := make([]byte, size)
				utf8.EncodeRune(utf8Buf, unicode)
				ret.Write(utf8Buf)
				i += 4
			default:
				ret.WriteByte(s[i])
			}
		} else {
			ret.WriteByte(s[i])
		}
	}
	return ret.String(), nil
}

// extract is used by Extract.
// NOTE: the return value will share something with j.
func extract(j JSON, pathExpr PathExpression) (ret []JSON) {
	if len(pathExpr.legs) == 0 {
		return []JSON{j}
	}
	currentLeg, subPathExpr := pathExpr.popOneLeg()
	if currentLeg.typ == pathLegIndex && j.typeCode == typeCodeArray {
		if currentLeg.arrayIndex == arrayIndexAsterisk {
			for _, child := range j.array {
				ret = append(ret, extract(child, subPathExpr)...)
			}
		} else if currentLeg.arrayIndex < len(j.array) {
			childRet := extract(j.array[currentLeg.arrayIndex], subPathExpr)
			ret = append(ret, childRet...)
		}
	} else if currentLeg.typ == pathLegKey && j.typeCode == typeCodeObject {
		if len(currentLeg.dotKey) == 1 && currentLeg.dotKey[0] == '*' {
			var sortedKeys = getSortedKeys(j.object) // iterate over sorted keys.
			for _, child := range sortedKeys {
				ret = append(ret, extract(j.object[child], subPathExpr)...)
			}
		} else if child, ok := j.object[currentLeg.dotKey]; ok {
			childRet := extract(child, subPathExpr)
			ret = append(ret, childRet...)
		}
	} else if currentLeg.typ == pathLegDoubleAsterisk {
		ret = append(ret, extract(j, subPathExpr)...)
		if j.typeCode == typeCodeArray {
			for _, child := range j.array {
				ret = append(ret, extract(child, pathExpr)...)
			}
		} else if j.typeCode == typeCodeObject {
			var sortedKeys = getSortedKeys(j.object)
			for _, child := range sortedKeys {
				ret = append(ret, extract(j.object[child], pathExpr)...)
			}
		}
	}
	return
}

// Merge merges suffixes into j according the following rules:
// 1) adjacent arrays are merged to a single array;
// 2) adjacent object are merged to a single object;
// 3) a scalar value is autowrapped as an array before merge;
// 4) an adjacent array and object are merged by autowrapping the object as an array.
func (j *JSON) Merge(suffixes []JSON) {
	switch j.typeCode {
	case typeCodeArray, typeCodeObject:
	default:
		firstElem := *j
		*j = CreateJSON(nil)
		j.typeCode = typeCodeArray
		j.array = []JSON{firstElem}
	}
	for i := 0; i < len(suffixes); i++ {
		suffix := suffixes[i]
		switch j.typeCode {
		case typeCodeArray:
			if suffix.typeCode == typeCodeArray {
				// rule (1)
				for _, elem := range suffix.array {
					j.array = append(j.array, elem)
				}
			} else {
				// rule (3), (4)
				j.array = append(j.array, suffix)
			}
		case typeCodeObject:
			if suffix.typeCode == typeCodeObject {
				// rule (2)
				for key := range suffix.object {
					j.object[key] = suffix.object[key]
				}
			} else {
				// rule (4)
				firstElem := *j
				*j = CreateJSON(nil)
				j.typeCode = typeCodeArray
				j.array = []JSON{firstElem}
				i--
			}
		}
	}
	return
}

// ModifyType is for modify a JSON. There are three valid values:
// ModifyInsert, ModifyReplace and ModifySet.
type ModifyType byte

const (
	// ModifyInsert is for insert a new element into a JSON.
	ModifyInsert ModifyType = 0x01
	// ModifyReplace is for replace an old elemList from a JSON.
	ModifyReplace ModifyType = 0x02
	// ModifySet = ModifyInsert | ModifyReplace
	ModifySet ModifyType = 0x03
)

// SetInsertReplace modifies a JSON object by insert, replace or set.
// All path expressions cannot contains * or ** wildcard.
func (j *JSON) SetInsertReplace(pathExprList []PathExpression, values []JSON, mt ModifyType) (err error) {
	if len(pathExprList) != len(values) {
		// TODO should return 1582(42000)
		return errors.New("Incorrect parameter count")
	}
	for i := 0; i < len(pathExprList); i++ {
		pathExpr := pathExprList[i]
		if pathExpr.flags.containsAnyAsterisk() {
			// TODO should return 3149(42000)
			return errors.New("Invalid path expression")
		}
		value := values[i]
		*j = set(*j, pathExpr, value, mt)
	}
	return
}

func set(j JSON, pathExpr PathExpression, value JSON, mt ModifyType) JSON {
	if len(pathExpr.legs) == 0 {
		return value
	}
	currentLeg, subPathExpr := pathExpr.popOneLeg()
	if currentLeg.typ == pathLegIndex && j.typeCode == typeCodeArray {
		var index = currentLeg.arrayIndex
		if len(j.array) > index && mt&ModifyReplace != 0 {
			j.array[index] = set(j.array[index], subPathExpr, value, mt)
		} else if len(subPathExpr.legs) == 0 && mt&ModifyInsert != 0 {
			j.array = append(j.array, value)
		}
	} else if currentLeg.typ == pathLegKey && j.typeCode == typeCodeObject {
		var key = currentLeg.dotKey
		if child, ok := j.object[key]; ok && mt&ModifyReplace != 0 {
			j.object[key] = set(child, subPathExpr, value, mt)
		} else if len(subPathExpr.legs) == 0 && mt&ModifyInsert != 0 {
			j.object[key] = value
		}
	}
	return j
}
