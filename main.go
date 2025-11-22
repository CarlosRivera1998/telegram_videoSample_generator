package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var BotToken = "YOUR_BOT_TOKEN_HERE"

type MergeSession struct {
	NumFiles    int
	Received    []*tgbotapi.Message
	ReceivedMux sync.Mutex
	CreatedAt   time.Time
}

type TrimSession struct {
	FileMsg   *tgbotapi.Message
	StartTime string
	EndTime   string
	CreatedAt time.Time
}

var mergeSessions = struct {
	m map[int64]*MergeSession
	sync.Mutex
}{m: make(map[int64]*MergeSession)}

var trimSessions = struct {
	m map[int64]*TrimSession
	sync.Mutex
}{m: make(map[int64]*TrimSession)}

func main() {
	if BotToken == "" {
		fmt.Println("Set BotToken variable in the source before running")
		return
	}

	bot, err := tgbotapi.NewBotAPI(BotToken)
	if err != nil {
		panic(err)
	}

	bot.Debug = false
	fmt.Printf("Authorized on account %s\n", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)

	// background cleaner for stale sessions
	go sessionCleaner()

	for upd := range updates {
		if upd.Message != nil {
			go handleMessage(bot, upd.Message)
			continue
		}
		if upd.CallbackQuery != nil {
			go handleCallback(bot, upd.CallbackQuery)
			continue
		}
	}
}

func sessionCleaner() {
	for {
		time.Sleep(5 * time.Minute)
		now := time.Now()
		mergeSessions.Lock()
		for k, v := range mergeSessions.m {
			if now.Sub(v.CreatedAt) > 30*time.Minute {
				delete(mergeSessions.m, k)
			}
		}
		mergeSessions.Unlock()

		trimSessions.Lock()
		for k, v := range trimSessions.m {
			if now.Sub(v.CreatedAt) > 30*time.Minute {
				delete(trimSessions.m, k)
			}
		}
		trimSessions.Unlock()
	}
}

func handleCallback(bot *tgbotapi.BotAPI, cq *tgbotapi.CallbackQuery) {
	data := cq.Data
	chatID := cq.Message.Chat.ID
	userID := cq.From.ID

	switch data {
	case "video_sample_generator":
		msg := tgbotapi.NewEditMessageText(chatID, cq.Message.MessageID, "Send a video file to generate a sample.")
		_, _ = bot.Send(msg)
	case "audio_trimmer":
		trimSessions.Lock()
		trimSessions.m[int64(userID)] = &TrimSession{CreatedAt: time.Now()}
		trimSessions.Unlock()
		msg := tgbotapi.NewEditMessageText(chatID, cq.Message.MessageID, "Send an audio file to trim.")
		_, _ = bot.Send(msg)
	case "audio_merger":
		mergeSessions.Lock()
		mergeSessions.m[int64(userID)] = &MergeSession{CreatedAt: time.Now()}
		mergeSessions.Unlock()
		msg := tgbotapi.NewEditMessageText(chatID, cq.Message.MessageID, "How many audio files would you like to merge? Send a number (e.g. 3).")
		_, _ = bot.Send(msg)
	default:
		// unknown
		_ = bot.AnswerCallbackQuery(tgbotapi.NewCallback(cq.ID, "Unknown action"))
	}
	_ = bot.AnswerCallbackQuery(tgbotapi.NewCallback(cq.ID, "")) // dismiss
}

func handleMessage(bot *tgbotapi.BotAPI, m *tgbotapi.Message) {
	chatID := m.Chat.ID
	userID := m.From.ID

	// commands
	if m.IsCommand() {
		switch m.Command() {
		case "start":
			kb := tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("Video Sample Generator", "video_sample_generator"),
				),
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("Audio Trimmer", "audio_trimmer"),
				),
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("Audio Merger", "audio_merger"),
				),
			)
			msg := tgbotapi.NewMessage(chatID, "Please choose an option:")
			msg.ReplyMarkup = kb
			_, _ = bot.Send(msg)
		default:
			_, _ = bot.Send(tgbotapi.NewMessage(chatID, "Unknown command. Use /start"))
		}
		return
	}

	// check if this user has an active merge session awaiting a number
	mergeSessions.Lock()
	ms, hasMerge := mergeSessions.m[int64(userID)]
	mergeSessions.Unlock()

	if hasMerge && ms.NumFiles == 0 {
		// expecting number
		if m.Text == "" {
			_, _ = bot.Send(tgbotapi.NewMessage(chatID, "Please send a number of files to merge."))
			return
		}
		n, err := strconv.Atoi(strings.TrimSpace(m.Text))
		if err != nil || n <= 0 {
			_, _ = bot.Send(tgbotapi.NewMessage(chatID, "Invalid number. Send a positive integer."))
			return
		}
		ms.NumFiles = n
		mergeSessions.Lock()
		mergeSessions.m[int64(userID)] = ms
		mergeSessions.Unlock()
		_, _ = bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("Please send %d audio files for merging.", n)))
		return
	}

	// check if this user has an active trim session awaiting timestamps
	trimSessions.Lock()
	ts, hasTrim := trimSessions.m[int64(userID)]
	trimSessions.Unlock()

	// if user has an active trim session and we are waiting for start/end times
	if hasTrim && ts.FileMsg != nil {
		// If start not set
		if ts.StartTime == "" {
			if m.Text == "" || !validateTimestamp(m.Text) {
				_, _ = bot.Send(tgbotapi.NewMessage(chatID, "Invalid timestamp format. Please send start time as HH:MM:SS"))
				return
			}
			ts.StartTime = m.Text
			trimSessions.Lock()
			trimSessions.m[int64(userID)] = ts
			trimSessions.Unlock()
			_, _ = bot.Send(tgbotapi.NewMessage(chatID, "Now send the end timestamp (HH:MM:SS) for trimming."))
			return
		}
		if ts.EndTime == "" {
			if m.Text == "" || !validateTimestamp(m.Text) {
				_, _ = bot.Send(tgbotapi.NewMessage(chatID, "Invalid timestamp format. Please send end time as HH:MM:SS"))
				return
			}
			ts.EndTime = m.Text
			trimSessions.Lock()
			trimSessions.m[int64(userID)] = ts
			trimSessions.Unlock()
			// proceed to trimming
			go processTrimAudio(bot, chatID, int64(userID), ts)
			return
		}
	}

	// if message contains audio for merge or trim
	if m.Audio != nil || m.Voice != nil || m.Document != nil && isAudioDocument(m.Document) {
		// treat as audio file
		// first check merge session
		if hasMerge {
			ms.ReceivedMux.Lock()
			ms.Received = append(ms.Received, m)
			count := len(ms.Received)
			expected := ms.NumFiles
			ms.ReceivedMux.Unlock()
			_, _ = bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("Received %d/%d", count, expected)))
			if count >= expected {
				go processMerge(bot, chatID, int64(userID))
			}
			return
		}
		// check trim session (expecting a file)
		if hasTrim && ts.FileMsg == nil {
			ts.FileMsg = m
			trimSessions.Lock()
			trimSessions.m[int64(userID)] = ts
			trimSessions.Unlock()
			_, _ = bot.Send(tgbotapi.NewMessage(chatID, "Please send the start timestamp (HH:MM:SS) for trimming."))
			return
		}
		// else no session
		_, _ = bot.Send(tgbotapi.NewMessage(chatID, "No active session. Start with /start"))
		return
	}

	// if message contains a video
	if m.Video != nil || (m.Document != nil && isVideoDocument(m.Document)) {
		// If video sample requested earlier we don't track a state; just trim any incoming video to 15s random start.
		go processVideoTrim(bot, m)
		return
	}

	// generic fallback
	_, _ = bot.Send(tgbotapi.NewMessage(chatID, "Please use /start to choose an option, or send the requested files."))
}

func isAudioDocument(doc *tgbotapi.Document) bool {
	if doc == nil {
		return false
	}
	mime := strings.ToLower(doc.MimeType)
	return strings.HasPrefix(mime, "audio/") || strings.Contains(mime, "mpeg")
}

func isVideoDocument(doc *tgbotapi.Document) bool {
	if doc == nil {
		return false
	}
	mime := strings.ToLower(doc.MimeType)
	return strings.HasPrefix(mime, "video/") || strings.Contains(mime, "mp4")
}

func validateTimestamp(ts string) bool {
	_, err := time.Parse("15:04:05", ts)
	return err == nil
}

func processMerge(bot *tgbotapi.BotAPI, chatID int64, userID int64) {
	mergeSessions.Lock()
	ms, ok := mergeSessions.m[userID]
	if !ok {
		_, _ = bot.Send(tgbotapi.NewMessage(chatID, "No merge session found."))
		mergeSessions.Unlock()
		return
	}
	mergeSessions.Unlock()

	statusMsg, _ := bot.Send(tgbotapi.NewMessage(chatID, "Starting audio merge..."))
	// download files
	var paths []string
	for i, msg := range ms.Received {
		p, err := downloadTelegramFile(bot, msg)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("Error downloading file %d: %v", i+1, err)))
			cleanupFiles(paths)
			return
		}
		paths = append(paths, p)
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("Downloaded %d/%d", i+1, ms.NumFiles)))
	}

	// create list file for ffmpeg concat demuxer if formats same, else re-encode/concat with filter_complex
	concatList := filepath.Join(os.TempDir(), fmt.Sprintf("concat_%d.txt", userID))
	f, err := os.Create(concatList)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("File error: %v", err)))
		cleanupFiles(paths)
		return
	}
	w := bufio.NewWriter(f)
	for _, p := range paths {
		_, _ = w.WriteString(fmt.Sprintf("file '%s'\n", escapePathForFFmpeg(p)))
	}
	_ = w.Flush()
	_ = f.Close()

	outFile := filepath.Join(os.TempDir(), fmt.Sprintf("merged_%d.mp3", userID))

	// try concat demuxer first (works if same codec/container)
	cmd := exec.Command("ffmpeg", "-y", "-f", "concat", "-safe", "0", "-i", concatList, "-c", "copy", outFile)
	err = cmd.Run()
	if err != nil {
		// fallback: re-encode using filter_complex concat
		args := []string{"-y"}
		for _, p := range paths {
			args = append(args, "-i", p)
		}
		args = append(args, "-filter_complex", fmt.Sprintf("concat=n=%d:v=0:a=1", len(paths)), "-c:a", "libmp3lame", outFile)
		cmd2 := exec.Command("ffmpeg", args...)
		if out, e2 := cmd2.CombinedOutput(); e2 != nil {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("ffmpeg failed: %v\n%s", e2, string(out))))
			cleanupFiles(paths)
			os.Remove(concatList)
			return
		}
	}

	// send file
	audioMsg := tgbotapi.NewAudio(chatID, tgbotapi.FilePath(outFile))
	audioMsg.Caption = "Merged audio"
	_, err = bot.Send(audioMsg)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("Upload failed: %v", err)))
	}

	// finalize
	cleanupFiles(paths)
	os.Remove(concatList)
	os.Remove(outFile)

	_, _ = bot.Send(tgbotapi.NewMessage(chatID, "Merged audio uploaded!"))

	// remove session
	mergeSessions.Lock()
	delete(mergeSessions.m, userID)
	mergeSessions.Unlock()

	// edit status
	_, _ = bot.Send(tgbotapi.NewEditMessageText(chatID, statusMsg.MessageID, "Merging completed."))
}

func processTrimAudio(bot *tgbotapi.BotAPI, chatID int64, userID int64, ts *TrimSession) {
	statusMsg, _ := bot.Send(tgbotapi.NewMessage(chatID, "Downloading audio..."))
	msg := ts.FileMsg
	p, err := downloadTelegramFile(bot, msg)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("Download error: %v", err)))
		return
	}
	_, _ = bot.Send(tgbotapi.NewMessage(chatID, "Audio downloaded. Trimming..."))

	outFile := filepath.Join(os.TempDir(), fmt.Sprintf("trimmed_%d_%s", userID, filepath.Base(p)))

	// ffmpeg: -ss start -to end -i input -c copy output
	cmd := exec.Command("ffmpeg", "-y", "-i", p, "-ss", ts.StartTime, "-to", ts.EndTime, "-c", "copy", outFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("ffmpeg error: %v\n%s", err, string(out))))
		os.Remove(p)
		return
	}

	_, _ = bot.Send(tgbotapi.NewMessage(chatID, "Trimming completed. Uploading..."))
	audioMsg := tgbotapi.NewAudio(chatID, tgbotapi.FilePath(outFile))
	audioMsg.Caption = "Trimmed audio"
	_, err = bot.Send(audioMsg)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("Upload failed: %v", err)))
	}

	os.Remove(p)
	os.Remove(outFile)

	_, _ = bot.Send(tgbotapi.NewMessage(chatID, "Trimmed audio uploaded!"))

	trimSessions.Lock()
	delete(trimSessions.m, userID)
	trimSessions.Unlock()

	_, _ = bot.Send(tgbotapi.NewEditMessageText(chatID, statusMsg.MessageID, "Done"))
}

func processVideoTrim(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	statusMsg, _ := bot.Send(tgbotapi.NewMessage(chatID, "Downloading video..."))

	p, err := downloadTelegramFile(bot, msg)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("Download error: %v", err)))
		return
	}
	_, _ = bot.Send(tgbotapi.NewMessage(chatID, "Video downloaded. Trimming..."))

	// determine duration via ffprobe (optional) or use message.Video.Duration
	duration := 0
	if msg.Video != nil {
		duration = msg.Video.Duration
	}
	// fallback safe start
	var startSeconds float64
	if duration > 15 {
		rand.Seed(time.Now().UnixNano())
		startSeconds = rand.Float64() * float64(duration-15)
	} else {
		startSeconds = 0
	}
	startStr := fmt.Sprintf("%.2f", startSeconds)
	outFile := filepath.Join(os.TempDir(), fmt.Sprintf("trimmed_vid_%d_%s", time.Now().Unix(), filepath.Base(p)))

	// ffmpeg: -ss start -i input -t 15 -c copy
	cmd := exec.Command("ffmpeg", "-y", "-ss", startStr, "-i", p, "-t", "15", "-c", "copy", outFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("ffmpeg error: %v\n%s", err, string(out))))
		os.Remove(p)
		return
	}

	_, _ = bot.Send(tgbotapi.NewMessage(chatID, "Trimming completed. Uploading..."))
	videoMsg := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(outFile))
	videoMsg.Caption = "Trimmed video (15s)"
	_, err = bot.Send(videoMsg)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("Upload failed: %v", err)))
	}

	os.Remove(p)
	os.Remove(outFile)
	_, _ = bot.Send(tgbotapi.NewEditMessageText(chatID, statusMsg.MessageID, "Trimmed video uploaded!"))
}

func downloadTelegramFile(bot *tgbotapi.BotAPI, m *tgbotapi.Message) (string, error) {
	var fileID string
	var fileName string

	if m.Audio != nil {
		fileID = m.Audio.FileID
		fileName = m.Audio.FileName
	}
	if m.Voice != nil {
		fileID = m.Voice.FileID
		// voice doesn't have filename; create one
		fileName = fmt.Sprintf("voice_%d.ogg", time.Now().UnixNano())
	}
	if m.Document != nil {
		// may be audio or video document
		fileID = m.Document.FileID
		if m.Document.FileName != "" {
			fileName = m.Document.FileName
		} else {
			fileName = fmt.Sprintf("doc_%d", time.Now().UnixNano())
		}
	}
	if m.Video != nil {
		fileID = m.Video.FileID
		// video file name
		fileName = fmt.Sprintf("video_%d.mp4", time.Now().UnixNano())
	}
	if fileID == "" {
		return "", errors.New("no file id found in message")
	}

	file, err := bot.GetFile(tgbotapi.FileConfig{FileID: fileID})
	if err != nil {
		return "", err
	}

	// create output path
	outPath := filepath.Join(os.TempDir(), fmt.Sprintf("%d_%s", time.Now().UnixNano(), sanitizeFilename(fileName)))

	downloadURL := file.Link(BotToken)
	// HTTP GET with progress (basic)
	resp, err := http.Get(downloadURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	out, err := os.Create(outPath)
	if err != nil {
		return "", err
	}
	defer out.Close()

	// copy by chunks so large files stream
	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return "", err
	}
	return outPath, nil
}

func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.ReplaceAll(name, "\\", "-")
	return name
}

func cleanupFiles(list []string) {
	for _, p := range list {
		_ = os.Remove(p)
	}
}

func escapePathForFFmpeg(p string) string {
	// ffmpeg concat expects paths; if any single quote, escape by replacing ' with '\''
	if strings.Contains(p, "'") {
		return strings.ReplaceAll(p, "'", "'\\''")
	}
	return p
}
