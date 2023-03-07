package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/adrg/sysfont"
	"github.com/fogleman/gg"
	"github.com/gin-gonic/gin"
	"github.com/ka2n/ptouchgo"
	_ "github.com/ka2n/ptouchgo/conn/usb"
	"github.com/mpvl/unique"
	"html/template"
	"image"
	"image/png"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
)

func Router(r *gin.Engine) {
	r.GET("/", index)
	r.GET("/print", index)
}

type SafePrinter struct {
	lock      sync.Mutex
	ser       ptouchgo.Serial
	connected bool
}

var printer SafePrinter
var printerStatus *ptouchgo.Status
var usableFonts []string

func openPrinter(ser *ptouchgo.Serial) error {
	args := flag.Args()

	var err error
	if !printer.connected {
		*ser, err = ptouchgo.Open(args[0], 0, true)

		if err != nil {
			println("Failed to open printer:", err.Error())
			return (err)
		}
	}
	printer.connected = true

	fmt.Println("reading status")
	ser.RequestStatus()
	printerStatus, err = ser.ReadStatus()
	if err != nil {
		return err
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(printerStatus)

	err = ser.Close()
	if err != nil {
		return err
	}

	*ser, err = ptouchgo.Open(args[0], uint(printerStatus.TapeWidth), true)

	if printerStatus.Error1 != 0 {
		return fmt.Errorf("Printer error1 state: %d", printerStatus.Error1)
	}

	if printerStatus.Error2 != 0 {
		return fmt.Errorf("Printer error2 state: %d. Press powerbutton once-", printerStatus.Error2)
	}
	return nil
}

func createImage(text string, font_path string, fontsize int, vheight int) (error, *image.Image) {
	fmt.Printf("creating image h= %d\n", vheight)
	var err error
	fontdata := goregular.TTF

	if font_path != "" {
		fontdata, err = ioutil.ReadFile(font_path)
		if err != nil {
			return fmt.Errorf("Could not read font: %v", err), nil
		}
	}

	// load the font with the freetype library
	f, err := opentype.Parse(fontdata)
	if err != nil {
		return fmt.Errorf("Could not parse font: %v", err), nil
	}

	face, err := opentype.NewFace(f, &opentype.FaceOptions{
		Size:    float64(fontsize),
		DPI:     72, // 72 is default value, as such fontsize 1:1 rendered pixels
		Hinting: font.HintingNone,
	})
	if err != nil {
		return err, nil
	}

	dc := gg.NewContext(100, 100)
	dc.SetFontFace(face)

	w, h := dc.MeasureString(text)
	fmt.Printf("width: %f; height: %f;\n", w, h)

	dc = gg.NewContext(int(w+40), vheight)
	dc.SetRGB(1, 1, 1)
	dc.Clear()
	dc.SetRGB(0, 0, 0)
	dc.SetFontFace(face)

	measure := font.MeasureString(face, text)
	metrics := face.Metrics()
	v_pos := float64(dc.Height())/2 + (float64(metrics.CapHeight)/64)/2

	fmt.Printf("v_pos %f / advance %f / font metric: %#v\n", v_pos, float64(measure), metrics)
	// canvas_height/2 + (ascend / 2)
	dc.DrawStringAnchored(text, (w+40)/2, v_pos, 0.5, 0)
	img := dc.Image()
	return nil, &img
}

func printLabel(chain bool, img *image.Image, ser *ptouchgo.Serial) error {
	if printerStatus == nil || printer.connected == false {
		return fmt.Errorf("Cannot print without printer")
	}

	if printerStatus.TapeWidth == 0 {
		return fmt.Errorf("Cannot print without tape detected")
	}

	dc := gg.NewContext((*img).Bounds().Dx(), 128)
	dc.SetRGB(1, 1, 1)
	dc.Clear()
	dc.DrawImageAnchored(*img, 0, 128/2, 0, 0.5)

	data, bytesWidth, err := ptouchgo.LoadRawImage(dc.Image(), printerStatus.TapeWidth)
	if err != nil {
		return err
	}
	rasterLines := len(data) / bytesWidth
	// Set property
	err = ser.SetPrintProperty(rasterLines)
	if err != nil {
		return err
	}
	packedData, err := ptouchgo.CompressImage(data, bytesWidth)
	if err != nil {
		return err
	}

	err = ser.SetRasterMode()
	if err != nil {
		return (err)
	}

	err = ser.SetFeedAmount(0)
	if err != nil {
		return (err)
	}

	err = ser.SetCompressionModeEnabled(true)
	if err != nil {
		return (err)
	}

	err = ser.SetPrintMode(true, false)
	if err != nil {
		return (err)
	}

	highDPI := true
	err = ser.SetExtendedMode(false, !chain, false, highDPI, false)
	if err != nil {
		return (err)
	}

	err = ser.SendImage(packedData)
	if err != nil {
		return err
	}

	err = ser.PrintAndEject()
	if err != nil {
		return err
	}

	return nil
}

func to_base64(img *image.Image) string {
	buf := new(bytes.Buffer)
	png.Encode(buf, *img)

	mimeType := "data:image/png;base64,"
	base := base64.StdEncoding.EncodeToString(buf.Bytes())

	return mimeType + base
}

func index(c *gin.Context) {
	var err error
	status := gin.H{}
	should_print := c.Request.URL.Path == "/print"

	label := c.Query("label")
	font := c.Query("font")
	count := c.DefaultQuery("count", "1")
	defaultFontSize := 32
	if printerStatus != nil && printerStatus.TapeWidth != 0 {
		// margin seems to scale with 128px max tape width
		if printerStatus.TapeWidth == 9 {
			defaultFontSize = 32
		} else if printerStatus.TapeWidth == 12 {
			defaultFontSize = 48
		} else {
			defaultFontSize = int(48 / 12 * printerStatus.TapeWidth)
		}
	}

	fontsize := c.DefaultQuery("fontsize", strconv.Itoa(defaultFontSize))
	chain_print := c.Query("chain")

	fmt.Printf("label: %s; count: %s; should_print =%b path=%s\n", label, count, should_print, c.Request.URL.Path)

	size := 0
	if fontsize == "" {
		fontsize = strconv.Itoa(defaultFontSize)
		size = defaultFontSize
	} else {
		size, err = strconv.Atoi(fontsize)
		if err != nil {
			size = defaultFontSize
			fontsize = strconv.Itoa(size)
		}
	}
	if size > 240 {
		size = 240
		fontsize = strconv.Itoa(size)
	}

	vmargin_px := 0

	printer.lock.Lock()
	defer printer.lock.Unlock()

	err = openPrinter(&printer.ser)
	if err != nil {
		status["err"] = err
		if printer.connected {
			printer.ser.Close()
		}
		printer.connected = false
	}
	printer.connected = false
	if printerStatus != nil && printerStatus.Model != 0 {
		status["connected"] = true
		if printerStatus.TapeWidth != 0 {
			// margin seems to scale with 128px max tape width
			vmargin_px = int(128 * printerStatus.TapeWidth / 24)
		}
		printer.connected = true
	} else {
		// pretend 12mm tape
		vmargin_px = int(128 * 12 / 24)
	}

	status["label"] = label
	fontPath := ""

	finder := sysfont.NewFinder(nil)

	status["fonts"] = usableFonts
	if strings.TrimSpace(font) != "" {
		foundFont := finder.Match(font)
		if foundFont != nil {
			fontPath = foundFont.Filename
			font = path.Base(fontPath)
			fmt.Printf("Found '%s' in '%s'\n", font, fontPath)
		} else {
			status["err"] = err
			fontPath = ""
		}
	}

	status["font"] = font

	err, img := createImage(label, fontPath, size, vmargin_px)
	if err != nil {
		status["err"] = err
	}

	if count == "" {
		count = "1"
	}

	copies, err := strconv.Atoi(count)
	if err != nil {
		copies = 1
	}

	if should_print {
		for i := 1; i <= copies; i++ {
			err = printLabel(i != copies || chain_print == "checked", img, &printer.ser)
			if err != nil {
				status["err"] = err
				break
			}
		}
	}
	if should_print {
		url := "/?"
		paramPairs := c.Request.URL.Query()
		for key, values := range paramPairs {
			url += key + "=" + values[0] + "&"
		}
		c.Redirect(http.StatusFound, url)
		return
	}

	status["count"] = count
	status["fontsize"] = fontsize

	if chain_print == "checked" {
		status["chain"] = should_print
	}

	if img != nil {
		// see issue https://github.com/golang/go/issues/20536 on why using URL type
		status["image"] = template.URL(to_base64(img))
	}

	if printerStatus != nil {
		status["status"] = printerStatus
	}
	fmt.Printf("template status: %v\n", status)

	c.HTML(
		http.StatusOK,
		"index",
		status,
	)
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: ptouch-web [device]\n")
	fmt.Fprintf(os.Stderr, "device can be \"usb\" or \"/dev/rfcomm0\" or similiar\n")
	flag.PrintDefaults()
	os.Exit(2)
}

func main() {
	flag.Usage = usage
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		fmt.Println("connection is missing.")
		os.Exit(1)
	}

	finder := sysfont.NewFinder(nil)
	for _, systemFont := range finder.List() {
		ext := path.Ext(systemFont.Filename)
		if systemFont.Name != "" && (ext == ".ttf" || ext == ".otf") {
			usableFonts = append(usableFonts, systemFont.Name)
		}
	}
	sort.Strings(usableFonts)
	unique.Strings(&usableFonts)

	r := gin.New()
	r.Use(gin.Logger())
	r.Use(gin.Recovery())

	r.Static("/css", "./static/css")
	r.Static("/js", "./static/js")
	r.Static("/img", "./static/img")

	r.LoadHTMLGlob("templates/*")
	Router(r)

	log.Println("Server started")
	r.Run() // listen and serve on 0.0.0.0:8080 (for windows "localhost:8080")
}
