# Go GC and Memory Layout: Important Performance Pitfalls

## Overview

When profiling Go applications with `pprof`, high CPU usage in:

- `runtime.gcBgMarkWorker`
- `runtime.scanobject`
- `runtime.greyobject`

usually indicates that the Go garbage collector is spending significant time in the **mark phase** traversing object references.

This document summarizes important implementation and design considerations for reducing GC overhead in high-performance systems such as databases, caches, query engines, and distributed systems.

---

# 1. What `runtime.gcBgMarkWorker` Actually Does

`runtime.gcBgMarkWorker` is a background goroutine used by Go's concurrent mark-and-sweep garbage collector.

Its primary responsibilities are:

1. Traverse reachable object graphs
2. Scan pointer fields
3. Mark reachable objects
4. Push discovered objects into GC work queues

Conceptually:

```text
root -> objA -> objB -> objC
             \
              -> objD
```

The GC repeatedly traverses these references.

Therefore:

> High CPU usage in `gcBgMarkWorker` usually means reference traversal is expensive.

---

# 2. GC Cost Depends More on Live Heap Than Allocation Volume

A critical point:

GC cost is usually dominated by:

- live heap size
- object graph complexity
- pointer count

rather than simply total allocation throughput.

Example:

- Large caches
- Long-lived maps
- Session state
- AST retention
- Buffer pools

can dramatically increase mark cost.

---

# 3. `[]*Item` vs `[]Item`

This is one of the most important tradeoffs in Go performance engineering.

---

## `[]*Item`

Example:

```go
items := []*Item
```

Advantages:

- Cheap copying (only pointer copies)
- Large structs avoid memcopy cost
- Easy mutation semantics

Disadvantages:

- Each object is separately allocated
- GC must traverse every pointer
- Poor cache locality
- Increased allocator pressure
- Increased object count

Object graph:

```text
slice
 ├─ ptr -> object
 ├─ ptr -> object
 ├─ ptr -> object
```

This increases GC traversal cost significantly.

---

## `[]Item`

Example:

```go
items := []Item
```

Advantages:

- Fewer heap objects
- Better cache locality
- Reduced GC metadata
- Lower pointer traversal cost
- Better memory density

Layout:

```text
[Item][Item][Item][Item]
```

Disadvantages:

- Value copies may become expensive
- Large structs can increase `memmove` / `duffcopy` overhead

---

# 4. Slice Assignment Does NOT Copy the Backing Array

Example:

```go
a := []Item{...}
b := a
```

This copies only the slice header:

```go
struct {
    ptr *Item
    len int
    cap int
}
```

The backing array remains shared.

---

# 5. Value Extraction DOES Copy Struct Data

Example:

```go
x := items[i]
```

If `Item` is large:

```go
type Item struct {
    Buf [4096]byte
}
```

then the entire struct is copied.

This may increase:

- `memmove`
- `duffcopy`

CPU consumption.

---

# 6. Cache Locality Matters Enormously

`[]Item` often outperforms `[]*Item` despite extra copies because:

- contiguous memory improves cache hit rate
- fewer pointer dereferences
- fewer TLB misses
- better CPU prefetch behavior

Pointer chasing can become more expensive than copying.

---

# 7. Common GC Hotspots

Frequent causes of heavy GC marking:

- Large maps
- Linked structures
- Trees
- `interface{}`
- Small heap objects
- Pointer-heavy structs
- `[]*T`
- Deep object graphs

---

# 8. Pointer Density Is Extremely Important

GC work is proportional to pointer scanning.

Example:

```go
type A struct {
    p1 *X
    p2 *Y
    p3 *Z
}
```

is more expensive than:

```go
type B struct {
    a int64
    b int64
    c [256]byte
}
```

Go GC can skip pointer-free regions efficiently.

---

# 9. Recommended Design Patterns

## Use Value Types for Small Structs

Good candidate:

```go
type Item struct {
    ID uint64
    X  uint32
    Y  uint32
}
```

---

## Separate Metadata From Payload

Instead of:

```go
type Item struct {
    Payload [8192]byte
}
```

prefer:

```go
type Item struct {
    Offset uint32
    Length uint32
}
```

Store payloads in:

- arenas
- slabs
- pooled buffers
- page allocators

---

## Use Indexes Instead of Pointers

Common in database engines.

Instead of:

```go
*Node
```

use:

```go
nodes[id]
```

Benefits:

- fewer heap objects
- reduced GC traversal
- improved locality
- easier arena allocation

---

## Return Pointers Only at API Boundaries

Internal representation:

```go
[]Item
```

External API:

```go
func Get(i int) *Item {
    return &items[i]
}
```

This is a common compromise.

---

# 10. Profiling Signals

## GC Traversal Problems

Common indicators:

```text
runtime.gcBgMarkWorker
runtime.scanobject
runtime.greyobject
```

This usually means:

- too many live objects
- excessive pointer graphs
- poor memory layout

---

## Excessive Copying

Common indicators:

```text
memmove
duffcopy
```

This usually means:

- large value copies
- oversized structs
- inefficient pass-by-value usage

---

# 11. Recommended Diagnostic Commands

Enable GC tracing:

```bash
GODEBUG=gctrace=1
```

Useful metrics:

- GC frequency
- heap growth
- mark time
- assist ratio

---

Compare allocation vs live heap:

```bash
go tool pprof -alloc_space
```

vs

```bash
go tool pprof -inuse_space
```

Interpretation:

- Large alloc + small inuse:
  short-lived allocation pressure

- Large inuse:
  long-lived heap retention causing GC cost

---

# 12. Key Engineering Principle

For high-performance Go systems:

> Reducing pointer-rich live object graphs is often more important than reducing allocation rate.

This is one of the central ideas behind:

- arena allocation
- slab allocators
- object pooling
- columnar layouts
- database memory contexts
- offset/index-based designs
