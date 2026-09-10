package tools

/*
	| Buffer Size | Elements | Memory at 64 B/element |
	|-------------|----------|------------------------|
	| `2<<10`     | 2,048    | 128 KiB                |
	| `2<<11`     | 4,096    | 256 KiB                |
	| `2<<12`     | 8,192    | 512 KiB                |
	| `2<<13`     | 16,384   | 1 MiB                  |
	| `2<<14`     | 32,768   | 2 MiB                  |
	| `2<<15`     | 65,536   | 4 MiB                  |
	| `2<<16`     | 131,072  | 8 MiB                  |
*/

const (
	BufferSize2048   = 2 << 10
	BufferSize4096   = 2 << 11
	BufferSize8192   = 2 << 12
	BufferSize16384  = 2 << 13
	BufferSize32768  = 2 << 14
	BufferSize65536  = 2 << 15
	BufferSize131072 = 2 << 16
)
