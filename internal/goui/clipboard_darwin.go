package goui

// Native screenshots and images copied in Preview are usually TIFF rather than
// PNG. Inspect advertised types without converting/decoding pixels on Update.
func clipboardHasImage() bool {
	pool := macSend(macClass("NSAutoreleasePool"), "new")
	defer macSend(pool, "release")
	types := macSend(macSend(macClass("NSPasteboard"), "generalPasteboard"), "types")
	for i := 0; i < int(macSend(types, "count")); i++ {
		switch macString(macSend(types, "objectAtIndex:", i)) {
		case "public.png", "public.tiff", "public.jpeg", "public.heic", "public.heif", "com.compuserve.gif", "com.microsoft.bmp", "public.webp":
			return true
		}
	}
	return false
}
