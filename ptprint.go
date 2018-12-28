package main

import "bytes"
import "encoding/base64"
import "encoding/binary"
import "errors"
import "fmt"
import "html"
import "image"
import _ "image/png"
import "io"
import "log"
import "net/http"
import "os"
import "os/exec"
import "strings"
import "time"

// Much of this was based on ptprint.rb, linked from
// http://www.undocprint.org/formats/page_description_languages/brother_p-touch
// as well as the other docs linked from that page

func write(f *os.File, b []byte) error {
	n, err := f.Write(b)
	if n != len(b) || err != nil {
		return fmt.Errorf("failed writing, wrote %d bytes, err %v", n, err)
	}
	// If I don't do this, I see regular EOFs when trying to read e.g. the printer's
	// status reply.  I'm guessing it doesn't have the world's fastest processor.
	// The actual delay here is plucked out of thin air, but is not painfully long
	// but long enough to (apparently) work.
	time.Sleep(time.Duration(len(b)) * time.Millisecond)
	return nil
}

// Status is the somewhat insane mostly-zeroes status reply from the printer.
// All of the fields prefixed "res" are marked "reserved" in the documentation,
// although some of them have actual meanings.
type Status struct {
	PrintHeadMark byte
	Size          byte
	ResFixed1     byte
	ResFixed2     byte
	ResHWVersion  byte
	ResFixed3     byte
	ResZero0      byte
	ResZero1      byte
	Error1        byte
	Error2        byte
	MediaWidth    byte
	Mediatype     byte
	ResZero2      byte
	ResZero3      byte
	ResZero4      byte
	ResZero5      byte
	ResZero6      byte
	MediaLength   byte
	StatusType    byte
	PhaseType     byte
	PhaseHigh     byte
	PhaseLow      byte
	NotifNum      byte
	ResZero7      byte
	ResZero8      byte
	ResZero9      byte
	ResZeroA      byte
	ResZeroB      byte
	ResZeroC      byte
	ResZeroD      byte
	ResZeroE      byte
	ResZeroF      byte
}

func readStatus(f *os.File) (*Status, error) {
	s := Status{}
	var err error
	// With a freshly-started printer, this often takes a couple of retries
	for i := 0; i < 10; i++ {
		err = binary.Read(f, binary.LittleEndian, &s)
		if err == nil || err != io.EOF {
			break
		}
		log.Printf("EOF reading status, try %d", i)
		time.Sleep(time.Second)
	}
	if err != nil {
		return nil, fmt.Errorf("Could not read status: %v", err)
	}
	return &s, nil
}

func checkStatus(s *Status) error {
	if s.PrintHeadMark != 0x80 {
		return fmt.Errorf("wanted PrintHeadMark 0x80, got 0x%02X", s.PrintHeadMark)
	}
	if s.Size != 32 {
		return fmt.Errorf("wanted Size 32, got %d", s.Size)
	}
	if s.ResFixed1 != 0x42 {
		return fmt.Errorf("wanted Fixed1 0x42, got 0x%02X", s.ResFixed1)
	}
	if s.ResFixed2 != 0x30 {
		return fmt.Errorf("wanted Fixed2 0x30, got 0x%02X", s.ResFixed2)
	}
	if s.ResHWVersion != 0x5a {
		return fmt.Errorf("wanted ResHWVersion 0x5a, got 0x%02X", s.ResHWVersion)
	}
	if s.ResFixed3 != 0x30 {
		return fmt.Errorf("wanted Fixed3 0x30, got 0x%02X", s.ResFixed3)
	}
	if (s.Error1 & 0x01) != 0x00 {
		return errors.New("no print media")
	}
	if (s.Error1 & 0x02) != 0x00 {
		return errors.New("end of print media")
	}
	if (s.Error1 & 0x04) != 0x00 {
		return errors.New("tape cutter jam")
	}
	if s.Error1 != 0x00 {
		return fmt.Errorf("unknown Error1 %02X", s.Error1)
	}
	if (s.Error2 & 0x04) != 0x00 {
		return errors.New("transmission error")
	}
	if (s.Error2 & 0x10) != 0x00 {
		return errors.New("cover open")
	}
	if (s.Error2 & 0x40) != 0x00 {
		return errors.New("cannot feed print media")
	}
	if s.Error2 != 0x00 {
		return fmt.Errorf("unknown ErrorInfo2 %02X", s.Error2)
	}
	return nil
}

func initPrinter(devicePath string) (*os.File, int, error) {
	f, err := os.OpenFile(devicePath, os.O_RDWR, 0)
	if err != nil {
		return nil, 0, fmt.Errorf("unable to open printer %s: %v", devicePath, err)
	}

	start := make([]byte, 200)
	reset := []byte{0x1B, '@'}
	getStatus := []byte{0x1B, 'i', 'S'}
	setAutoCut := []byte{0x1B, 'i', 'M', 0x48} // Auto cut, small feed amount
	setFullCut := []byte{0x1B, 'i', 'K', 0x08} // Cut all the way through after every print
	setCompression := []byte{'M', 0x02}        // Use RLE compression (which we won't actually do, but whatever)

	err = write(f, start)
	if err != nil {
		return nil, 0, fmt.Errorf("unable to start communication: %v", err)
	}

	err = write(f, reset)
	if err != nil {
		return nil, 0, fmt.Errorf("unable to reset printer: %v", err)
	}

	err = write(f, getStatus)
	if err != nil {
		return nil, 0, fmt.Errorf("unable to ask printer for status: %v", err)
	}

	s, err := readStatus(f)
	if err != nil {
		return nil, 0, fmt.Errorf("error reading printer status: %v", err)
	}

	err = checkStatus(s)
	if err != nil {
		return nil, 0, fmt.Errorf("printer reports error: %v", err)
	}

	err = write(f, setAutoCut)
	if err != nil {
		return nil, 0, fmt.Errorf("unable to set auto-cut: %v", err)
	}

	err = write(f, setFullCut)
	if err != nil {
		return nil, 0, fmt.Errorf("unable to set full cut: %v", err)
	}

	err = write(f, setCompression)
	if err != nil {
		return nil, 0, fmt.Errorf("unable to set compression: %v", err)
	}
	return f, int(s.MediaWidth), nil
}

func mediaWidthToPixels(w int) int {
	switch w {
	case 9:
		return 64
	default:
		return 128
	}
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "<html>\n<head>\n<title>Label printer</title>\n<body>\n<form action='/preview' method='post'>Text to print:<textarea name='text' rows='4' cols='50'></textarea>\n<input type='submit' value='Preview'>\n")
}

type previewHandler struct {
	mediaWidth int
}

func (h *previewHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	text := r.FormValue("text")

	s := fmt.Sprintf("x%d", mediaWidthToPixels(h.mediaWidth))
	c := exec.Command("convert", "+antialias", "-background", "white", "-fill", "black", "-size", s, "-gravity", "South", "label:"+text, "png:-")
	log.Printf("Preview running command '%v'", c)
	png, err := c.Output()
	if err != nil {
		w.WriteHeader(502)
		fmt.Fprintf(w, "Error generating preview: %v", err)
		return
	}

	htmltext := html.EscapeString(text)
	htmltext = strings.Replace(htmltext, "\n", "<br />", -1)
	fmt.Fprintf(w, "<html>\n<head>\n<title>Label preview</title>\n<body>\n<form action='/print' method='post'><input type='hidden' name='text' value='%s'>\n", html.EscapeString(text))
	fmt.Fprintf(w, "<img border=1 alt='%s' src='data:image/png;base64,", htmltext)
	fmt.Fprintf(w, "%s", base64.StdEncoding.EncodeToString(png))
	fmt.Fprintf(w, "' />\n")
	fmt.Fprintf(w, "<p>%s</p><input type='submit' value='Print'>\n", htmltext)
}

type printHandler struct {
	printer    *os.File
	mediaWidth int
}

func (h *printHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	text := r.FormValue("text")

	s := fmt.Sprintf("x%d", mediaWidthToPixels(h.mediaWidth))
	c := exec.Command("convert", "+antialias", "-background", "white", "-fill", "black", "-size", s, "-gravity", "South", "-rotate", "-90", "label:"+text, "png:-")
	log.Printf("Print running command '%v'", c)
	png, err := c.Output()
	if err != nil {
		w.WriteHeader(502)
		fmt.Fprintf(w, "Error generating print data: %v", err)
		return
	}
	pngr := bytes.NewReader(png)

	pngc, ifmt, err := image.DecodeConfig(pngr)
	if err != nil {
		w.WriteHeader(502)
		fmt.Fprintf(w, "Error decoding print PNG config: %v", err)
		return
	}

	_, err = pngr.Seek(0, 0)
	if err != nil {
		w.WriteHeader(502)
		fmt.Fprintf(w, "Error seeking print PNG: %v", err)
		return
	}

	pngi, ifmt, err := image.Decode(pngr)
	if err != nil {
		w.WriteHeader(502)
		fmt.Fprintf(w, "Error decoding print PNG: %v", err)
		return
	}
	if ifmt != "png" {
		w.WriteHeader(502)
		fmt.Fprintf(w, "Error with print PNG, want format 'png', got '%s'", ifmt)
		return
	}
	log.Printf("Read print data in format '%s', width %d, height %d", ifmt, pngc.Width, pngc.Height)

	padLeft := (128 - pngc.Width) / 2
	padRight := 128 - (pngc.Width + padLeft)
	for y := pngc.Height - 1; y >= 0; y-- {
		line := make([]byte, 128/8+4) // The printer always wants 128 pixels of data, but for narrow band, only prints the middle bit
		line[0] = 'G'
		line[1] = 0x11
		line[2] = 0x00
		line[3] = 0x0f
		lc := 4
		for x := 0; x < padLeft; x += 8 {
			line[lc] = byte(0)
			lc++
		}
		for x := 0; x < pngc.Width; x += 8 {
			by := uint32(0)
			for b := 0; b < 8; b++ {
				pr, _, _, _ := pngi.At(x+b, y).RGBA()
				if pr == 0 {
					by = by | (128 >> uint(b))
				}
			}
			line[lc] = byte(by)
			lc++
		}
		for x := 0; x < padRight; x += 8 {
			line[lc] = byte(0)
			lc++
		}
		write(h.printer, line)
	}
	endPrint := []byte{0x1a}
	write(h.printer, endPrint)

	htmltext := html.EscapeString(text)
	htmltext = strings.Replace(htmltext, "\n", "<br />", -1)
	fmt.Fprintf(w, "<html>\n<head>\n<title>Label print</title>\n<body>\n")
	fmt.Fprintf(w, "<img border=1 alt='%s' src='data:image/png;base64,", htmltext)
	fmt.Fprintf(w, "%s", base64.StdEncoding.EncodeToString(png))
	fmt.Fprintf(w, "' />\n")
	fmt.Fprintf(w, "<p>%s</p>\n", htmltext)
}

func main() {
	f, mediaWidth, err := initPrinter("/dev/usb/lp1")
	if err != nil {
		log.Fatalf("Could not initialize printer: %v", err)
	}
	log.Printf("Printer initialized successfully.  Media width is %dmm.\n", mediaWidth)
	http.HandleFunc("/", rootHandler)
	http.Handle("/preview", &previewHandler{mediaWidth})
	http.Handle("/print", &printHandler{f, mediaWidth})
	http.ListenAndServe(":40404", nil)
}
