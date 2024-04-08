package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/color"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
)

const (
	WIDTH  = 120
	HEIGHT = 80
)

func BoxFilter(img *image.NRGBA, bounds image.Rectangle) color.NRGBA {
	n := uint(bounds.Size().X * bounds.Size().Y)

	if n == 0 {
		return color.NRGBA{}
	}

	var r, g, b uint
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			c := img.NRGBAAt(x, y)
			r += uint(c.R)
			g += uint(c.G)
			b += uint(c.B)
		}
	}

	return color.NRGBA{
		uint8(r / n),
		uint8(g / n),
		uint8(b / n),
		0,
	}
}

func Downscale(original *image.NRGBA, resized *image.NRGBA) {
	originalSize := original.Bounds().Size()
	targetSize := resized.Bounds().Size()

	var ratio float64

	if originalSize.X == originalSize.Y {
		// square
		minDimension := min(targetSize.X, targetSize.Y)
		ratio = float64(originalSize.X) / float64(minDimension)
	} else if originalSize.X > originalSize.Y {
		// horizontally oriented
		ratio = float64(originalSize.X) / float64(targetSize.X)
	} else {
		// vertically oriented
		ratio = float64(originalSize.Y) / float64(targetSize.Y)
	}

	for y := 0; y < targetSize.Y; y++ {
		originalY := int(math.Floor(float64(y) * ratio))

		for x := 0; x < targetSize.X; x++ {
			originalX := int(math.Floor(float64(x) * ratio))

			resized.SetNRGBA(
				x,
				y,
				BoxFilter(
					original,
					image.Rect(
						originalX,
						originalY,
						int(math.Ceil(float64(originalX)+ratio)),
						int(math.Ceil(float64(originalY)+ratio)),
					).Intersect(original.Bounds())),
			)
		}
	}
}

type Parameter int

const (
	FOREGROUND = Parameter(38)
	BACKGROUND = Parameter(48)
)

func StackPixels(top color.NRGBA, bottom color.NRGBA) string {
	EscSequence := func(parameter Parameter, rgb color.NRGBA, content string) string {
		return fmt.Sprintf(
			"\u001b[%d;2;%d;%d;%dm%s\u001b[0m",
			parameter,
			rgb.R, rgb.G, rgb.B,
			content,
		)
	}

	fg := EscSequence(FOREGROUND, top, "\u2580")
	return EscSequence(BACKGROUND, bottom, fg)
}

func GetDimensions(path string) (*image.Point, error) {
	cmd := exec.Command(
		"ffprobe",
		"-i", path,
		"-show_streams",
		"-select_streams", "v",
		"-loglevel", "quiet",
		"-output_format", "compact",
	)

	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	pattern := regexp.MustCompile(`width=(\d+)\|height=(\d+)`)
	matches := pattern.FindStringSubmatch(string(out))

	size := image.Point{}

	if len(matches) >= 3 {
		size.X, _ = strconv.Atoi(matches[1])
		size.Y, _ = strconv.Atoi(matches[2])
	}

	return &size, nil
}

func UrlFrameRunner(url string, size image.Point, framesChannel chan []byte) {
	ytdl := exec.Command(
		"youtube-dl",
		"-o", "-",
		url,
		"-f", "worst",
	)

	ffmpeg := exec.Command(
		"ffmpeg",
		"-i", "pipe:0",
		"-s", fmt.Sprintf("%dx%d", size.X, size.Y),
		"-loglevel", "quiet",
		"-pix_fmt", "rgb0",
		"-vcodec", "rawvideo",
		"-f", "image2pipe",
		"-",
	)

	in, out := io.Pipe()
	defer out.Close()

	ytdl.Stdout = out
	ffmpeg.Stdin = in

	stdout, _ := ffmpeg.StdoutPipe()

	ytdl.Start()
	ffmpeg.Start()

	frame := make([]byte, size.X*size.Y*4)

	for {
		_, err := io.ReadFull(stdout, frame)
		if err != nil {
			break
		}

		framesChannel <- frame
	}

	ytdl.Wait()
	ffmpeg.Wait()
	close(framesChannel)
}

func FileFrameRunner(path string, size image.Point, framesChannel chan []byte) {
	cmd := exec.Command(
		"ffmpeg",
		"-i", path,
		"-loglevel", "quiet",
		"-pix_fmt", "rgb0",
		"-vcodec", "rawvideo",
		"-f", "image2pipe",
		"-",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Fatalf("Failed to connect stdout pipe for ffmpeg")
	}

	err = cmd.Start()
	if err != nil {
		log.Fatalf("Failed to start ffmpeg command")
	}

	frame := make([]byte, size.X*size.Y*4)

	for {
		_, err := io.ReadFull(stdout, frame)
		if err != nil {
			break
		}

		framesChannel <- frame
	}

	cmd.Wait()
	close(framesChannel)
}

var path string
var url string

func init() {
	flag.StringVar(&path, "path", "", "path to video file")
	flag.StringVar(&url, "url", "", "url of a video source")
}

func main() {
	flag.Parse()

	var size image.Point
	framesChannel := make(chan []byte)

	clear := exec.Command("clear")
	clear.Stdout = os.Stdout
	clear.Run()

	if path != "" {
		dim, _ := GetDimensions(path)
		size.X = dim.X
		size.Y = dim.Y

		go FileFrameRunner(path, size, framesChannel)
	} else if url != "" {
		size.X = WIDTH
		size.Y = HEIGHT

		go UrlFrameRunner(url, size, framesChannel)
	} else {
		log.Println("Incorrect usage")
		flag.Usage()
		os.Exit(1)
	}

	white := color.NRGBA{255, 255, 255, 255}
	frameBuffer := bytes.NewBuffer(
		make([]byte, 0, len(StackPixels(white, white))*WIDTH*HEIGHT/2),
	)

	original := image.NewNRGBA(image.Rect(0, 0, size.X, size.Y))
	resized := image.NewNRGBA(image.Rect(0, 0, WIDTH, HEIGHT))
	bounds := resized.Rect

	for {
		fmt.Print("\u001b[H")

		frame, ok := <-framesChannel
		if !ok {
			break
		}

		original.Pix = frame
		Downscale(original, resized)

		for y := bounds.Min.Y; y < bounds.Max.Y; y += 2 {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				top := resized.NRGBAAt(x, y)
				bot := resized.NRGBAAt(x, y+1)
				frameBuffer.WriteString(StackPixels(top, bot))
			}
			frameBuffer.WriteByte('\n')
		}

		io.Copy(os.Stdout, frameBuffer)
		frameBuffer.Reset()
	}
}
