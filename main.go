package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"image"
	"image/png"
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/fogleman/gg"
	"github.com/gin-gonic/gin"
	"github.com/ka2n/ptouchgo"
	_ "github.com/ka2n/ptouchgo/conn/usb"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
)

func Router(r *gin.Engine) {
	r.GET("/", index)
	r.GET("/print", index)

}

var ser ptouchgo.Serial
var printerStatus *ptouchgo.Status
var lastImage image.Image

func openPrinter() error {
	args := flag.Args()

	var err error
	ser, err = ptouchgo.Open(args[0], 0, true)

	if err != nil {
		return (err)
	}

	err = ser.Reset()
	if err != nil {
		return (err)
	}

	fmt.Println("reading status")
	ser.RequestStatus()
	printerStatus, err = ser.ReadStatus()
	if err != nil {
		return err
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(printerStatus)

	return nil
}

func createImage(text string, fontsize int, vheight int) {
	fmt.Printf("creating image h= %d\n", vheight)

	f, err := opentype.Parse(goregular.TTF)
	if err != nil {
		panic("")
	}

	face, err := opentype.NewFace(f, &opentype.FaceOptions{
		Size:    float64(fontsize),
		DPI:     72, // 72 is default value, as such fontsize 1:1 rendered pixels
		Hinting: font.HintingNone,
	})
	if err != nil {
		panic("")
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
	lastImage = dc.Image()
}

func printLabel(chain bool) error {
	dc := gg.NewContext(lastImage.Bounds().Dx(), 128)
	dc.SetRGB(1, 1, 1)
	dc.Clear()
	dc.DrawImageAnchored(lastImage, 0, 128/2, 0, 0.5)

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

	err = ser.Reset()
	if err != nil {
		return (err)
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
	status := gin.H{}
	should_print := c.Request.URL.Path == "/print"

	label := c.Query("label")
	count := c.DefaultQuery("count", "1")
	fontsize := c.DefaultQuery("fontsize", "48")
	chain_print := c.Query("chain")

	fmt.Printf("label: %s; count: %s; should_print =%s path=%s\n", label, count, should_print, c.Request.URL.Path)

	if fontsize == "" {
		fontsize = "48"
	}

	size, err := strconv.Atoi(fontsize)

	if err != nil {
		size = 48
		fontsize = strconv.Itoa(size)
	}

	if size > 240 {
		size = 240
		fontsize = strconv.Itoa(size)
	}

	vmargin_px := 32 // default for 12mm label

	err = openPrinter()
	if err != nil {
		status["err"] = err
	} else if printerStatus.Model != 0 {
		status["connected"] = true
		if printerStatus.TapeWidth != 0 {
			// margin seems to scale with 128px max tape width
			vmargin_px = int(128 * printerStatus.TapeWidth / 24)
		}
	}
	status["label"] = label

	createImage(label, size, vmargin_px)

	if count == "" {
		count = "1"
	}

	copies, err := strconv.Atoi(count)
	if err != nil {
		copies = 1
	}

	if should_print {
		for i := 1; i <= copies; i++ {
			err = printLabel(i != copies || chain_print == "checked")
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

	if lastImage != nil {
		// see issue https://github.com/golang/go/issues/20536 on why using URL type
		status["image"] = template.URL(to_base64(&lastImage))
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
