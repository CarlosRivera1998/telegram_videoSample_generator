import logging
from pyrogram import Client, filters
from pyrogram.types import InlineKeyboardMarkup, InlineKeyboardButton
import os
import subprocess
import nest_asyncio
import time
import asyncio
import uvloop
from asyncio import get_event_loop_policy, set_event_loop_policy, DefaultEventLoopPolicy

# Set uvloop as the default event loop policy
set_event_loop_policy(uvloop.EventLoopPolicy())

# Logging Setup
logging.basicConfig(level=logging.INFO, format='%(asctime)s - %(name)s - %(levelname)s - %(message)s')
logger = logging.getLogger(__name__)

# Apply nest_asyncio
nest_asyncio.apply()

# Telegram API Credentials
api_id =   #dont use ""
api_hash = " "
bot_token = " "

# Initialize Pyrogram Client
app = Client("my_bot", api_id=api_id, api_hash=api_hash, bot_token=bot_token)

# Store message IDs for audio trimming and merging
audio_message_ids = {}
audio_files_to_merge = {}
audio_trim_sessions = {}

# Function to split a file into chunks
def split_file(input_file, chunk_size):
    with open(input_file, 'rb') as f:
        while True:
            chunk = f.read(chunk_size)
            if not chunk:
                break
            yield chunk

# Handler for /start command
@app.on_message(filters.command("start"))
async def start_command(client, message):
    keyboard = InlineKeyboardMarkup([
        [InlineKeyboardButton("Video Sample Generator", callback_data="video_sample_generator")],
        [InlineKeyboardButton("Audio Trimmer", callback_data="audio_trimmer")],
        [InlineKeyboardButton("Audio Merger", callback_data="audio_merger")]
    ])
    await message.reply("Please choose an option:", reply_markup=keyboard)

# Callback query handler
@app.on_callback_query(filters.regex(r"video_sample_generator"))
async def video_sample_generator(client, callback_query):
    await callback_query.message.edit_text("Send a video file to generate a sample.")

@app.on_callback_query(filters.regex(r"audio_trimmer"))
async def audio_trimmer(client, callback_query):
    user_id = callback_query.from_user.id
    audio_trim_sessions[user_id] = {'file': None, 'start_time': None, 'end_time': None}
    await callback_query.message.edit_text("Send an audio file to trim.")

@app.on_callback_query(filters.regex(r"audio_merger"))
async def audio_merger(client, callback_query):
    user_id = callback_query.from_user.id
    audio_files_to_merge[user_id] = {'num_files': None, 'received_files': []}
    await callback_query.message.edit_text("How many audio files would you like to merge? Please provide a number.")

# Custom filter to exclude commands
def exclude_commands(_, __, message):
    return not message.text.startswith("/")

# Handler for number of files for audio merger
@app.on_message(filters.text & filters.create(exclude_commands))
async def handle_number_of_files(client, message):
    user_id = message.from_user.id
    if user_id in audio_files_to_merge and audio_files_to_merge[user_id]['num_files'] is None:
        try:
            num_files = int(message.text)
            if num_files > 0:
                audio_files_to_merge[user_id]['num_files'] = num_files
                await message.reply(f"Please send {num_files} audio files for merging.")
            else:
                await message.reply("Please provide a valid number of files.")
        except ValueError:
            await message.reply("Please provide a valid number.")
    elif user_id in audio_trim_sessions and audio_trim_sessions[user_id]['file'] is None:
        await message.reply("Please send an audio file to trim first.")
    elif user_id in audio_trim_sessions and audio_trim_sessions[user_id]['start_time'] is None:
        if validate_timestamp(message.text):
            audio_trim_sessions[user_id]['start_time'] = message.text
            await message.reply("Now send the end timestamp (HH:MM:SS) for trimming.")
        else:
            await message.reply("Invalid timestamp format. Please use HH:MM:SS.")
    elif user_id in audio_trim_sessions and audio_trim_sessions[user_id]['end_time'] is None:
        if validate_timestamp(message.text):
            audio_trim_sessions[user_id]['end_time'] = message.text
            await trim_audio_file(client, message)
        else:
            await message.reply("Invalid timestamp format. Please use HH:MM:SS.")
    else:
        await message.reply("Please start by selecting an option using /start.")

# Handler for audio messages
@app.on_message(filters.audio)
async def handle_audio(client, message):
    user_id = message.from_user.id
    if user_id in audio_files_to_merge:
        if len(audio_files_to_merge[user_id]['received_files']) < audio_files_to_merge[user_id]['num_files']:
            audio_files_to_merge[user_id]['received_files'].append(message)
            if len(audio_files_to_merge[user_id]['received_files']) == audio_files_to_merge[user_id]['num_files']:
                await merge_audio_files(client, message)
        else:
            await message.reply("You have already sent the required number of files.")
    elif user_id in audio_trim_sessions:
        audio_trim_sessions[user_id]['file'] = message
        await message.reply("Please send the start timestamp (HH:MM:SS) for trimming.")
    else:
        await message.reply("Please select an option using /start first.")

# Function to validate timestamp format
def validate_timestamp(timestamp):
    try:
        time.strptime(timestamp, '%H:%M:%S')
        return True
    except ValueError:
        return False

# Function to merge audio files
async def merge_audio_files(client, message):
    user_id = message.from_user.id
    audio_messages = audio_files_to_merge[user_id]['received_files']
    output_file = f"merged_{user_id}.mp3"
    input_files = []

    try:
        status_msg = await message.reply("Starting audio merge...")

        # Download all audio files
        last_update_time = time.time()
        for i, audio_message in enumerate(audio_messages):
            async def progress_callback(current, total):
                nonlocal last_update_time
                if time.time() - last_update_time > 1:  # Update every 1 second
                    await status_msg.edit_text(f"Downloading audio {i+1}/{len(audio_messages)}... {current / total * 100:.1f}%")
                    last_update_time = time.time()
            file_path = await audio_message.download(progress=progress_callback)
            input_files.append(file_path)

        await status_msg.edit_text("All audio files downloaded.\nMerging audio files...")

        # Temporarily switch to the default event loop policy for handling subprocess
        original_policy = get_event_loop_policy()
        set_event_loop_policy(DefaultEventLoopPolicy())

        # Merge audio files using ffmpeg
        ffmpeg_cmd = ["ffmpeg"]
        for input_file in input_files:
            ffmpeg_cmd.extend(["-i", input_file])
        ffmpeg_cmd.extend(["-filter_complex", f"concat=n={len(input_files)}:v=0:a=1", "-c:a", "libmp3lame", output_file])
        process = await asyncio.create_subprocess_exec(
            *ffmpeg_cmd,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE
        )
        
        # Log ffmpeg output
        async for line in process.stderr:
            logger.info(line.decode().strip())
        await process.communicate()

        # Switch back to uvloop
        set_event_loop_policy(original_policy)

        await status_msg.edit_text("Merging completed.\nUploading merged audio...")

        # Upload merged audio
        await client.send_audio(
            chat_id=message.chat.id,
            audio=output_file,
            caption="Merged audio"
        )

        # Final update
        await status_msg.edit_text("Merged audio uploaded!")
    except Exception as e:
        logger.exception(f"Error processing audio: {e}")
        await status_msg.edit_text(f"An error occurred while processing your audio: {e}")
    finally:
        # Clean up
        for file in input_files:
            if os.path.exists(file):
                os.remove(file)
        if os.path.exists(output_file):
            os.remove(output_file)
        # Clear the stored data
        audio_files_to_merge.pop(user_id, None)

# Function to trim audio file
async def trim_audio_file(client, message):
    user_id = message.from_user.id
    audio_message = audio_trim_sessions[user_id]['file']
    start_time = audio_trim_sessions[user_id]['start_time']
    end_time = audio_trim_sessions[user_id]['end_time']
    status_msg = await message.reply("Downloading audio...")
    
    try:
        last_update_time = time.time()
        async def progress_callback(current, total):
            nonlocal last_update_time
            if time.time() - last_update_time > 1:  # Update every 1 second
                await status_msg.edit_text(f"Downloading audio... {current / total * 100:.1f}%")
                last_update_time = time.time()
        file_path = await audio_message.download(progress=progress_callback)

        await status_msg.edit_text("Audio downloaded.\nTrimming audio...")

        # Temporarily switch to the default event loop policy for handling subprocess
        original_policy = get_event_loop_policy()
        set_event_loop_policy(DefaultEventLoopPolicy())

        # Trim audio using ffmpeg
        output_file = "trimmed_" + os.path.basename(file_path)
        ffmpeg_cmd = ["ffmpeg", "-i", file_path, "-ss", start_time, "-to", end_time, "-c", "copy", output_file]
        process = await asyncio.create_subprocess_exec(
            *ffmpeg_cmd,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE
        )

        # Log ffmpeg output
        async for line in process.stderr:
            logger.info(line.decode().strip())
        await process.communicate()

        # Switch back to uvloop
        set_event_loop_policy(original_policy)

        await status_msg.edit_text("Trimming completed.\nUploading audio...")

        # Upload trimmed audio
        await client.send_audio(
            chat_id=message.chat.id,
            audio=output_file,
            caption="Trimmed audio"
        )

        # Final update
        await status_msg.edit_text("Trimmed audio uploaded!")
    except Exception as e:
        logger.exception(f"Error processing audio: {e}")
        await status_msg.edit_text(f"An error occurred while processing your audio: {e}")
    finally:
        # Clean up
        if os.path.exists(file_path):
            os.remove(file_path)
        if os.path.exists(output_file):
            os.remove(output_file)
        # Clear the session data
        audio_trim_sessions.pop(user_id, None)

# Handler for video messages
@app.on_message(filters.video)
async def handle_video(client, message):
    status_msg = await message.reply("Downloading video...")
    
    try:
        last_update_time = time.time()
        async def progress_callback(current, total):
            nonlocal last_update_time
            if time.time() - last_update_time > 1:  # Update every 1 second
                await status_msg.edit_text(f"Downloading video... {current / total * 100:.1f}%")
                last_update_time = time.time()
        file_path = await message.download(progress=progress_callback)

        await status_msg.edit_text("Video downloaded.\nTrimming video...")

        # Temporarily switch to the default event loop policy for handling subprocess
        original_policy = get_event_loop_policy()
        set_event_loop_policy(DefaultEventLoopPolicy())

        # Trim video using ffmpeg
        start_time = random.uniform(0, max(0, message.video.duration - 15))
        output_file = "trimmed_" + os.path.basename(file_path)
        ffmpeg_cmd = ["ffmpeg", "-ss", str(start_time), "-i", file_path, "-t", "15", "-c", "copy", output_file]
        process = await asyncio.create_subprocess_exec(
            *ffmpeg_cmd,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE
        )
        
        # Log ffmpeg output
        async for line in process.stderr:
            logger.info(line.decode().strip())
        await process.communicate()

        # Switch back to uvloop
        set_event_loop_policy(original_policy)

        await status_msg.edit_text("Trimming completed.\nUploading video...")

        # Upload trimmed video
        await client.send_video(
            chat_id=message.chat.id,
            video=output_file,
            caption="Trimmed video"
        )

        # Final update
        await status_msg.edit_text("Trimmed video uploaded!")
    except Exception as e:
        logger.exception(f"Error processing video: {e}")
        await status_msg.edit_text(f"An error occurred while processing your video: {e}")
    finally:
        # Clean up
        if os.path.exists(file_path):
            os.remove(file_path)
        if os.path.exists(output_file):
            os.remove(output_file)

# Main function to start the bot
async def main():
    try:
        await app.start()
        logger.info("Bot is running. Waiting for messages...")
        while True:
            await asyncio.sleep(1)  # Sleep for a short duration
    except Exception as e:
        logger.exception(f"An error occurred: {e}")
    finally:
        await app.stop()

if __name__ == "__main__":
    asyncio.run(main())

