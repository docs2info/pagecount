// pages.go counts pages by walking the page tree (ISO 32000-1 §7.7.3). The trailer's /Root names
// the document catalog (§7.7.2), whose /Pages names the root of the tree. From three.pdf:
//
//	1 0 obj
//	<< /Type /Catalog /Pages 2 0 R >>
//	endobj
//	2 0 obj
//	<< /Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 3 >>
//	endobj
//
// A /Type /Pages node is an inner node: /Kids lists its children and /Count states how many pages
// lie below it. A /Type /Page node, such as object 3, is a leaf: one page. CountPages calls walk
// to count the leaves from the root and compares the result with the root's /Count.

package pdf

import (
	"errors"
	"fmt"
)

// CountPages returns the number of pages in the PDF in b. A page is a dictionary reached from the
// trailer /Root → /Pages, following /Kids, whose /Type is /Page; CountPages counts those leaves.
// It returns an error rather than a guess when the file is encrypted, has no startxref, /Root or
// /Pages don't resolve, the page tree is malformed (see walk), or the root /Count is missing, not
// an integer, or out of range. If the root /Count disagrees with the walk it returns the walked
// count and a "count mismatch" error.
func CountPages(b []byte) (int, error) {
	d, err := openDocument(b)
	if err != nil {
		return 0, err
	}
	root, err := d.resolve(d.trailer["Root"])
	if err != nil {
		return 0, fmt.Errorf("unresolvable /Root: %w", err)
	}
	cat, ok := root.(Dict)
	if !ok {
		return 0, errors.New("unresolvable /Root")
	}
	top, err := d.resolve(cat["Pages"])
	if err != nil {
		return 0, fmt.Errorf("unresolvable /Pages: %w", err)
	}
	tree, ok := top.(Dict)
	if !ok {
		return 0, errors.New("unresolvable /Pages")
	}
	walked, err := d.walk(cat["Pages"])
	if err != nil {
		return 0, err
	}
	cv, err := d.resolve(tree["Count"])
	if err != nil {
		return 0, fmt.Errorf("/Pages /Count: %w", err)
	}
	if cv == nil {
		return 0, errors.New("/Pages has no /Count")
	}
	if _, isInt := cv.(Number); !isInt {
		return 0, errors.New("/Count is not an integer")
	}
	c, ok := asInt(cv)
	if !ok {
		return 0, errors.New("/Count out of range")
	}
	if c != walked {
		return walked, fmt.Errorf("count mismatch: /Count=%d walked=%d", c, walked)
	}
	return walked, nil
}

// walk counts the /Type /Page leaves of the tree rooted at top, which must be a /Type /Pages node.
// /Type alone classifies a node: /Pages needs a /Kids array, /Page must have no /Kids, anything else
// is an error, as is a kid that isn't a dictionary or a reference to a free or missing object. A
// node reached twice through different parents counts once; one reached again from below itself is
// a cycle and an error. The walk is iterative, so tree depth is bounded by memory, not the stack.
func (d *document) walk(top any) (int, error) {
	type frame struct {
		ref   Ref
		isRef bool
		kids  Array
		next  int
	}
	var stack []frame
	seen := map[Ref]bool{}   // every referenced node reached so far
	onPath := map[Ref]bool{} // referenced /Pages nodes from the root to the current node
	n := 0
	enter := func(o any, root bool) error {
		r, isRef := o.(Ref)
		if isRef {
			if onPath[r] {
				return fmt.Errorf("page tree: cycle through %d %d R", r.Num, r.Gen)
			}
			if seen[r] {
				return nil
			}
			seen[r] = true
		}
		node, err := d.node(o)
		if err != nil {
			return err
		}
		typ, err := d.resolve(node["Type"])
		if err != nil {
			return fmt.Errorf("page tree /Type: %w", err)
		}
		switch {
		case typ == Name("Pages"):
			kv, err := d.resolve(node["Kids"])
			if err != nil {
				return fmt.Errorf("page tree /Kids: %w", err)
			}
			kids, ok := kv.(Array)
			if !ok {
				return errors.New("page tree: /Kids is not an array")
			}
			if isRef {
				onPath[r] = true
			}
			stack = append(stack, frame{ref: r, isRef: isRef, kids: kids})
		case root:
			return fmt.Errorf("page tree: root /Type is %v, want /Pages", typeName(typ))
		case typ == Name("Page"):
			if _, ok := node["Kids"]; ok {
				return errors.New("page tree: /Page node has /Kids")
			}
			n++
		case typ == nil:
			return errors.New("page tree: node has no /Type")
		default:
			return fmt.Errorf("page tree: node /Type is %v, want /Page or /Pages", typeName(typ))
		}
		return nil
	}
	if err := enter(top, true); err != nil {
		return 0, err
	}
	for len(stack) > 0 {
		f := &stack[len(stack)-1]
		if f.next == len(f.kids) {
			if f.isRef {
				delete(onPath, f.ref)
			}
			stack = stack[:len(stack)-1]
			continue
		}
		kid := f.kids[f.next]
		f.next++ // before enter, whose append may move the stack
		if err := enter(kid, false); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// node resolves a page tree node to its dictionary.
func (d *document) node(o any) (Dict, error) {
	v, err := d.resolve(o)
	if err != nil {
		return nil, fmt.Errorf("page tree: %w", err)
	}
	if node, ok := v.(Dict); ok {
		return node, nil
	}
	if r, ok := o.(Ref); ok && v == nil {
		if e, ok := d.xref[r.Num]; ok && e.typ == 0 {
			return nil, fmt.Errorf("page tree: free object referenced: %d %d R", r.Num, r.Gen)
		}
		return nil, fmt.Errorf("page tree: missing object referenced: %d %d R", r.Num, r.Gen)
	}
	return nil, errors.New("page tree: kid is not a dictionary")
}

// typeName formats a /Type value for an error message.
func typeName(v any) string {
	if n, ok := v.(Name); ok {
		return "/" + string(n)
	}
	return fmt.Sprint(v)
}
