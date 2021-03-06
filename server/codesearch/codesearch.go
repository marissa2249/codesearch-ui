/*
 * Copyright 2015 Dolphin Emulator project. All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *   http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package codesearch

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/google/codesearch/index"
	"github.com/google/codesearch/regexp"

	"kythe.io/kythe/go/services/web"
	"kythe.io/kythe/go/services/xrefs"
	xpb "kythe.io/kythe/proto/xref_proto"

	"golang.org/x/net/context"
)

// Service to search for text in a code repository based on regular
// expressions.
type Service interface {
	Search(context.Context, *CodeSearchRequest) (*CodeSearchReply, error)
}

type localImpl struct {
	xs xrefs.Service
	ix *index.Index
}

func (r *Regexp) Compile() (*regexp.Regexp, error) {
	if r.Expr == "" {
		return nil, errors.New("Empty search regexp provided.")
	}
	expr := r.Expr
	if r.CaseSensitive {
		expr = "(?i)" + expr
	}
	return regexp.Compile(expr)
}

func (s *localImpl) ReadFileContents(ctx context.Context, ticket string) ([]byte, error) {
	req := xpb.DecorationsRequest{
		Location: &xpb.Location{
			Ticket: ticket,
			Kind:   xpb.Location_FILE,
		},
		SourceText: true,
		References: false,
	}
	repl, err := s.xs.Decorations(ctx, &req)
	if err != nil {
		return nil, fmt.Errorf("Decorations request failed: %v", err)
	}
	return repl.SourceText, nil
}

func countNL(b []byte) int {
	n := 0
	for {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			break
		}
		n++
		b = b[i+1:]
	}
	return n
}

func GetSnippets(data []byte, re *regexp.Regexp, nSnippets int) []*Snippet {
	var (
		s          = []*Snippet{}
		chunkStart = 0
		lineNo     = 1
	)
	for chunkStart < len(data) {
		if len(s) >= nSnippets {
			break
		}
		mIdx := re.Match(data[chunkStart:], true, true) + chunkStart
		if mIdx < chunkStart {
			break
		}
		lineStart := bytes.LastIndex(data[chunkStart:mIdx], []byte("\n")) + 1 + chunkStart
		lineEnd := mIdx
		if lineEnd > len(data) {
			lineEnd = len(data)
		}
		lineNo += countNL(data[chunkStart:lineStart])
		line := string(data[lineStart:lineEnd])
		snip := &Snippet{
			Content:    line,
			LineNumber: int32(lineNo),
		}
		s = append(s, snip)
		chunkStart = lineEnd
	}
	return s
}

func (s *localImpl) Search(ctx context.Context, req *CodeSearchRequest) (*CodeSearchReply, error) {
	reply := CodeSearchReply{}
	if req.Regexp == nil {
		return nil, errors.New("No search regexp provided.")
	}
	re, err := req.Regexp.Compile()
	if err != nil {
		return nil, fmt.Errorf("Search regexp compilation error: %v", err)
	}
	q := index.RegexpQuery(re.Syntax)
	for _, fileid := range s.ix.PostingQuery(q) {
		// TODO(delroth): Apply pagination.
		// TODO(delroth): Sort the filenames by relevance.
		// TODO(delroth): File RE should be applied here.
		fn := s.ix.Name(fileid)
		data, err := s.ReadFileContents(ctx, fn)
		if err != nil {
			return nil, fmt.Errorf("Search in file contents failed: %v", err)
		}
		m := &Match{
			Filename: fn,
			Snippet:  GetSnippets(data, re, 5),
		}
		if len(m.Snippet) > 0 {
			reply.Match = append(reply.Match, m)
		}
	}
	return &reply, nil
}

func New(csPath string, xs xrefs.Service) Service {
	ix := index.Open(csPath)
	return &localImpl{xs, ix}
}

func RegisterHTTPHandlers(ctx context.Context, s Service, mux *http.ServeMux) {
	mux.HandleFunc("/codesearch", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		defer func() {
			log.Printf("codesearch.CodeSearch:\t%s", time.Since(start))
		}()

		var req CodeSearchRequest
		if err := web.ReadJSONBody(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		reply, err := s.Search(ctx, &req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := web.WriteResponse(w, r, reply); err != nil {
			log.Println(err)
		}
	})
}
