# Vendored zstd decompressor

The files in this directory are the decompressor from the Go standard
library's `internal/zstd` package, copied unmodified from Go 1.27.1.
They are pure Go and use only the standard library.

Copyright 2023 The Go Authors. All rights reserved.
Use of this source code is governed by a BSD-style license; see LICENSE.

The importer uses this package only for decompression. Recompression is
performed by the local `zstd` command-line tool and verified by decompressing
the result again with this package; exact compressed bytes are not required.
