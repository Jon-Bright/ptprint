package main

// Much of this was based on ptprint.rb, linked from
// http://www.undocprint.org/formats/page_description_languages/brother_p-touch
// as well as the other docs linked from that page

// 18mm tape
// 292 = 45mm = 0.1541
// 377 = 56.3mm = 0.1493
// 398 = 60mm = 0.1507
// 442 = 66mm = 0.1493

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"image"
	_ "image/png"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"
)

var dev = flag.String("dev", "/dev/usb/lp1", "The USB device of the label printer")
var port = flag.Int("port", 40404, "The port the server should listen on")
var convert = flag.String("convert", "/usr/bin/convert", "Path to ImageMagick's convert utility")

type TransientError int

const (
	AllIsWell TransientError = iota
	NoTapeCartridge
	TapeRanOut
	TapeJammed
	CoverOpen
)

type HWVersion byte

const (
	PT2430PC HWVersion = 0x5a
	PTP700   HWVersion = 0x67
)

type Printer struct {
	f          *os.File
	wc         chan []byte
	ec         chan error
	mediaWidth int
	te         TransientError
	hwVer      HWVersion
}

// MediaWidth returns the width of the currently-inserted media in mm
func (p *Printer) MediaWidth() int {
	return p.mediaWidth
}

func (p *Printer) TransientError() TransientError {
	return p.te
}

func (p *Printer) Status() string {
	switch p.te {
	case AllIsWell:
		return fmt.Sprintf("Printer OK<br />%dmm tape inserted", p.mediaWidth)
	case NoTapeCartridge:
		return "No tape inserted!"
	case TapeRanOut:
		return "The tape has run out!"
	case TapeJammed:
		return "The tape is jammed!"
	case CoverOpen:
		return "The printer's cover is open!"
	}
	return "Unknown transient error, this should never happen"
}

func (p *Printer) rawWrite(b []byte) error {
	n, err := p.f.Write(b)
	if n != len(b) || err != nil {
		return fmt.Errorf("failed writing, wrote %d bytes, err %v", n, err)
	}
	return nil
}

func (p *Printer) Write(b []byte) error {
	p.wc <- b
	return <-p.ec
}

// Status is the somewhat-silly mostly-zeroes status reply from the printer.
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

func (p *Printer) readStatus() (*Status, error) {
	s := Status{}
	var err error
	// The printer is slow and its USB interface is buggy.  This leads to
	// it wantonly returning EOF rather than blocking, even when data will
	// still be delivered.
	for i := 0; i < 10; i++ {
		err = binary.Read(p.f, binary.LittleEndian, &s)
		if err == nil || err != io.EOF {
			break
		}
		log.Printf("EOF reading status, try %d", i)
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return nil, fmt.Errorf("Could not read status: %v", err)
	}
	return &s, nil
}

func checkStatus(s *Status) (TransientError, error) {
	if s.PrintHeadMark != 0x80 {
		return 0, fmt.Errorf("wanted PrintHeadMark 0x80, got 0x%02X", s.PrintHeadMark)
	}
	if s.Size != 32 {
		return 0, fmt.Errorf("wanted Size 32, got %d", s.Size)
	}
	if s.ResFixed1 != 0x42 {
		return 0, fmt.Errorf("wanted Fixed1 0x42, got 0x%02X", s.ResFixed1)
	}
	if s.ResFixed2 != 0x30 {
		return 0, fmt.Errorf("wanted Fixed2 0x30, got 0x%02X", s.ResFixed2)
	}
	switch HWVersion(s.ResHWVersion) {
	case PT2430PC:
		log.Println("Hardware is a PT-2430PC")
	case PTP700:
		log.Println("Hardware is a PT-P700")
	default:
		return 0, fmt.Errorf("unknown ResHWVersion, got 0x%02X", s.ResHWVersion)
	}
	if s.ResFixed3 != 0x30 {
		return 0, fmt.Errorf("wanted Fixed3 0x30, got 0x%02X", s.ResFixed3)
	}
	var te TransientError
	if (s.Error1 & 0x01) != 0x00 {
		te = NoTapeCartridge
	} else if (s.Error1 & 0x02) != 0x00 {
		te = TapeRanOut
	} else if (s.Error1 & 0x04) != 0x00 {
		te = TapeJammed
	} else if s.Error1 != 0x00 {
		return 0, fmt.Errorf("unknown Error1 %02X", s.Error1)
	}
	if (s.Error2 & 0x04) != 0x00 {
		return 0, errors.New("transmission error")
	} else if (s.Error2 & 0x40) != 0x00 {
		return 0, errors.New("cannot feed print media")
	} else if (s.Error2 & 0x10) != 0x00 {
		if te == AllIsWell {
			te = CoverOpen
		}
	} else if s.Error2 != 0x00 {
		return 0, fmt.Errorf("unknown ErrorInfo2 %02X", s.Error2)
	}
	return te, nil
}

func (p *Printer) run() {
	for {
		select {
		case w := <-p.wc:
			err := p.rawWrite(w)
			p.ec <- err
		case <-time.After(10 * time.Second):
			var err error
			p.te, err = p.checkStatus()
			if err != nil {
				log.Fatalf("Regular status inquiry failed: %v", err)
			}
			log.Printf("Status OK, TransientError %v, Media width %d", p.te, p.mediaWidth)
		}
	}
}

func (p *Printer) checkStatus() (TransientError, error) {
	getStatus := []byte{0x1B, 'i', 'S'}
	err := p.rawWrite(getStatus)
	if err != nil {
		return 0, fmt.Errorf("unable to ask printer for status: %v", err)
	}

	s, err := p.readStatus()
	if err != nil {
		return 0, fmt.Errorf("error reading printer status: %v", err)
	}

	te, err := checkStatus(s)
	if err != nil {
		return 0, fmt.Errorf("printer reports error: %v", err)
	}
	p.mediaWidth = int(s.MediaWidth)
	p.hwVer = HWVersion(s.ResHWVersion)
	return te, nil
}

func NewPrinter(devicePath string) (*Printer, error) {
	f, err := os.OpenFile(devicePath, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("unable to open printer %s: %v", devicePath, err)
	}

	p := Printer{}
	p.f = f
	p.wc = make(chan []byte, 1)
	p.ec = make(chan error, 1)

	start := make([]byte, 200)
	reset := []byte{0x1B, '@'}
	setAutoCut := []byte{0x1B, 'i', 'M', 0x48} // Auto cut, small feed amount
	//setAutoCut := []byte{0x1B, 'i', 'M', 0x08} // Auto cut, small feed amount
	setFullCut := []byte{0x1B, 'i', 'K', 0x08} // Cut all the way through after every print
	setCompression := []byte{'M', 0x02}        // Use RLE compression (which we won't actually do, but whatever)

	err = p.rawWrite(start)
	if err != nil {
		return nil, fmt.Errorf("unable to start communication: %v", err)
	}

	err = p.rawWrite(reset)
	if err != nil {
		return nil, fmt.Errorf("unable to reset printer: %v", err)
	}

	p.te, err = p.checkStatus()
	if err != nil {
		return nil, fmt.Errorf("status problem: %v", err)
	}

	err = p.rawWrite(setAutoCut)
	if err != nil {
		return nil, fmt.Errorf("unable to set auto-cut: %v", err)
	}

	err = p.rawWrite(setFullCut)
	if err != nil {
		return nil, fmt.Errorf("unable to set full cut: %v", err)
	}

	err = p.rawWrite(setCompression)
	if err != nil {
		return nil, fmt.Errorf("unable to set compression: %v", err)
	}
	return &p, nil
}

func mediaWidthToPixels(w int) int {
	switch w {
	case 6:
		return 48
	case 9:
		return 64
	case 12:
		return 96
	default:
		return 128
	}
}

type rootHandler struct {
	p *Printer
	t *template.Template
}

func NewRootHandler(p *Printer) (*rootHandler, error) {
	rh := rootHandler{}
	rh.p = p
	var err error
	rh.t, err = template.ParseFiles("textform.html")
	if err != nil {
		return nil, fmt.Errorf("failed parsing root template: %v", err)
	}
	return &rh, nil
}

func (rh *rootHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d := struct {
		StatusOK bool
		Status   template.HTML
	}{
		rh.p.TransientError() == AllIsWell,
		template.HTML(rh.p.Status()),
	}
	rh.t.Execute(w, d)
}

type previewHandler struct {
	p *Printer
	t *template.Template
}

func NewPreviewHandler(p *Printer) (*previewHandler, error) {
	ph := previewHandler{}
	ph.p = p
	var err error
	ph.t, err = template.ParseFiles("preview.html")
	if err != nil {
		return nil, fmt.Errorf("failed parsing preview template: %v", err)
	}
	return &ph, nil
}

func (h *previewHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	text := r.FormValue("text")

	s := fmt.Sprintf("x%d", mediaWidthToPixels(h.p.MediaWidth()))
	c := exec.Command(*convert, "+antialias", "-background", "white", "-fill", "black", "-size", s, "-gravity", "South", "label:"+text, "png:-")
	log.Printf("Preview running command '%v'", c)
	png, err := c.Output()
	if err != nil {
		w.WriteHeader(502)
		fmt.Fprintf(w, "Error generating preview: %v", err)
		return
	}

	d := struct {
		StatusOK bool
		Status   template.HTML
		Text     string
		Image    string
	}{
		h.p.TransientError() == AllIsWell,
		template.HTML(h.p.Status()),
		text,
		base64.StdEncoding.EncodeToString(png),
	}
	h.t.Execute(w, d)
}

type printHandler struct {
	p *Printer
	t *template.Template
}

func NewPrintHandler(p *Printer) (*printHandler, error) {
	ph := printHandler{}
	ph.p = p
	var err error
	ph.t, err = template.ParseFiles("print.html")
	if err != nil {
		return nil, fmt.Errorf("failed parsing print template: %v", err)
	}
	return &ph, nil
}

func (h *printHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	text := r.FormValue("text")

	s := fmt.Sprintf("x%d", mediaWidthToPixels(h.p.MediaWidth()))
	c := exec.Command(*convert, "+antialias", "-background", "white", "-fill", "black", "-size", s, "-gravity", "South", "-rotate", "-90", "label:"+text, "png:-")
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
		err = h.p.Write(line)
		if err != nil {
			w.WriteHeader(502)
			fmt.Fprintf(w, "Error writing print data: %v", err)
			return
		}
	}
	endPrint := []byte{0x1a}
	err = h.p.Write(endPrint)
	if err != nil {
		w.WriteHeader(502)
		fmt.Fprintf(w, "Error writing end-of-print: %v", err)
		return
	}

	d := struct {
		StatusOK bool
		Status   template.HTML
		Text     string
		Image    string
	}{
		h.p.TransientError() == AllIsWell,
		template.HTML(h.p.Status()),
		text,
		base64.StdEncoding.EncodeToString(png),
	}
	h.t.Execute(w, d)
}

func main() {
	flag.Parse()
	p, err := NewPrinter(*dev)
	if err != nil {
		log.Fatalf("Could not initialize printer: %v", err)
	}
	log.Printf("Printer initialized successfully.  Media width is %dmm.\n", p.MediaWidth())
	go p.run()
	rh, err := NewRootHandler(p)
	if err != nil {
		log.Fatalf("Could not make root handler: %v", err)
	}
	http.Handle("/", rh)
	preh, err := NewPreviewHandler(p)
	if err != nil {
		log.Fatalf("Could not make preview handler: %v", err)
	}
	http.Handle("/preview", preh)
	prih, err := NewPrintHandler(p)
	if err != nil {
		log.Fatalf("Could not make print handler: %v", err)
	}
	http.Handle("/print", prih)
	http.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir("assets"))))
	http.ListenAndServe(fmt.Sprintf(":%d", *port), nil)
}
