package com.ellanetworks.mcptt.media

import android.annotation.SuppressLint
import android.media.AudioAttributes
import android.media.AudioFormat
import android.media.AudioRecord
import android.media.AudioTimestamp
import android.media.AudioTrack
import android.media.MediaCodec
import android.media.MediaFormat
import android.media.MediaRecorder
import android.os.Process
import android.os.SystemClock
import android.util.Log
import java.nio.ByteBuffer

private const val TAG = "EllaMcptt"
private const val PCM_BYTES = AmrWb.FRAME_SAMPLES * 2
private const val FRAME_US = 20_000L
private const val IN_TIMEOUT_US = 10_000L

/**
 * An AMR-WB codec of MediaCodec (CDD §5.1: every handheld has both), used synchronously a frame at a time: storage
 * frames in, PCM out for the decoder; PCM in, storage frames out for the encoder (RFC 4867 §5.3, the header octet of
 * each, no file header: AOSP's C2SoftAmrDec and C2SoftAmrWbEnc).
 */
private class Codec(encoder: Boolean, bitRate: Int) : AutoCloseable {
    private val codec: MediaCodec
    private val info = MediaCodec.BufferInfo()
    private var pts = 0L

    init {
        val format = MediaFormat.createAudioFormat(MediaFormat.MIMETYPE_AUDIO_AMR_WB, AmrWb.SAMPLE_RATE, 1)
        if (encoder) {
            format.setInteger(MediaFormat.KEY_BIT_RATE, bitRate)
            format.setInteger(MediaFormat.KEY_MAX_INPUT_SIZE, PCM_BYTES)
        }
        val type = MediaFormat.MIMETYPE_AUDIO_AMR_WB
        codec = if (encoder) MediaCodec.createEncoderByType(type) else MediaCodec.createDecoderByType(type)
        try {
            codec.configure(format, null, null, if (encoder) MediaCodec.CONFIGURE_FLAG_ENCODE else 0)
            codec.start()
        } catch (e: Exception) {
            codec.release()
            throw e
        }
    }

    /**
     * Gives [n] bytes of [input] to the codec, then each output ready to [out]: waiting [waitUs] for the first, the
     * codec's delay, so that the outputs keep the inputs' cadence.
     */
    fun process(input: ByteArray, n: Int, waitUs: Long, out: (ByteBuffer) -> Unit) {
        val i = codec.dequeueInputBuffer(IN_TIMEOUT_US)
        if (i >= 0) {
            val b = requireNotNull(codec.getInputBuffer(i))
            b.clear()
            b.put(input, 0, n)
            codec.queueInputBuffer(i, 0, n, pts, 0)
            pts += FRAME_US
        }
        var wait = waitUs
        while (true) {
            val o = codec.dequeueOutputBuffer(info, wait)
            if (o < 0) {
                if (o == MediaCodec.INFO_TRY_AGAIN_LATER) return
                continue
            }
            wait = 0
            val b = requireNotNull(codec.getOutputBuffer(o))
            b.position(info.offset)
            b.limit(info.offset + info.size)
            if (info.flags and MediaCodec.BUFFER_FLAG_CODEC_CONFIG == 0) out(b)
            codec.releaseOutputBuffer(o, false)
        }
    }

    override fun close() {
        runCatching { codec.stop() }
        codec.release()
    }
}

private val voice: AudioAttributes = AudioAttributes.Builder()
    .setUsage(AudioAttributes.USAGE_VOICE_COMMUNICATION)
    .setContentType(AudioAttributes.CONTENT_TYPE_SPEECH)
    .build()

private val pcm: AudioFormat = AudioFormat.Builder()
    .setEncoding(AudioFormat.ENCODING_PCM_16BIT)
    .setSampleRate(AmrWb.SAMPLE_RATE)
    .build()

/** The speech heard in a call ([Playout]'s). */
interface SpeechOut {
    fun start()

    fun stop()
}

/** The speech said in a call ([Capture]'s). */
interface SpeechIn {
    /** The microphone captures: its first frame read since started. */
    val ready: Boolean

    fun start(mode: Int)

    fun stop()

    fun send(on: Boolean)

    fun mode(mode: Int)
}

/**
 * The audio of the calls: of each its playout and capture, [failed] told once one failed and failed again when
 * rebuilt (Worker). [DeviceAudio] of the device, fakes in the tests.
 */
interface Audio {
    fun playout(next: () -> ByteArray, failed: (String) -> Unit): SpeechOut

    /** The capture of a call, [ready] told as it becomes so (SpeechIn.ready). */
    fun capture(send: (ByteArray) -> Unit, failed: (String) -> Unit, ready: () -> Unit): SpeechIn
}

object DeviceAudio : Audio {
    override fun playout(next: () -> ByteArray, failed: (String) -> Unit): SpeechOut = Playout(next, failed)

    override fun capture(send: (ByteArray) -> Unit, failed: (String) -> Unit, ready: () -> Unit): SpeechIn = Capture(send, failed, ready)
}

/** An error of the platform's audio: the track or the record to build again (AudioTrack.ERROR_DEAD_OBJECT). */
private class AudioError(message: String) : Exception(message)

/**
 * The speech heard in a call: a frame of the session's jitter buffer each 20 ms ([next]), decoded and played in the
 * call's audio (USAGE_VOICE_COMMUNICATION, routed by Telecom). The track's blocking writes pace the frames. NO_DATA
 * is decoded too, as silence, so that the frames keep their order through the codec's delay. A write that fails (the
 * track dead, of an audioserver restart or a route change) no more blocks: the track built again, not the frames spun.
 */
class Playout(private val next: () -> ByteArray, failed: (String) -> Unit) : SpeechOut {
    private val worker = Worker("playout", failed, ::run)

    override fun start() = worker.start()

    override fun stop() = worker.stop()

    private fun run(running: () -> Boolean) {
        val track = AudioTrack.Builder()
            .setAudioAttributes(voice)
            .setAudioFormat(AudioFormat.Builder(pcm).setChannelMask(AudioFormat.CHANNEL_OUT_MONO).build())
            .setPerformanceMode(AudioTrack.PERFORMANCE_MODE_LOW_LATENCY)
            .setBufferSizeInBytes(
                maxOf(
                    AudioTrack.getMinBufferSize(AmrWb.SAMPLE_RATE, AudioFormat.CHANNEL_OUT_MONO, AudioFormat.ENCODING_PCM_16BIT),
                    2 * PCM_BYTES,
                ),
            )
            .build()
        val pcmOut = ByteArray(PCM_BYTES * 2)
        val latency = Latency("playout")
        var frames = 0L
        var written = 0L
        try {
            Codec(encoder = false, bitRate = 0).use { decoder ->
                // The writes block once the track holds this much: what is heard lags the session by it. Two frames,
                // the least that never underruns between two 20 ms writes, whatever the buffer the track allocated
                // (AudioTrack.setBufferSizeInFrames).
                track.setBufferSizeInFrames(2 * AmrWb.FRAME_SAMPLES)
                track.play()
                while (running()) {
                    val frame = next()
                    frames++
                    decoder.process(frame, frame.size, FRAME_US) { b ->
                        val n = minOf(b.remaining(), pcmOut.size)
                        b.get(pcmOut, 0, n)
                        val w = track.write(pcmOut, 0, n, AudioTrack.WRITE_BLOCKING)
                        if (w < 0) throw AudioError("AudioTrack.write: $w")
                        written += n / 2
                    }
                    if (frames % LATENCY_EVERY == 0L) latency.playout(track, frames, written)
                }
            }
        } finally {
            latency.log()
            Log.i(TAG, "playout underruns: ${track.underrunCount}")
            runCatching { track.stop() }
            track.release()
        }
    }
}

/**
 * The speech said in a call: the microphone (VOICE_COMMUNICATION, its echo canceller and gain control where the device
 * has them) in frames of 20 ms, recorded while the call is active, encoded in the AMR-WB mode the session asks for and
 * each frame given to [send] while the client transmits ([send] of a Boolean). The recorder and the encoder are ready
 * before the floor is granted: starting them takes some 400 ms, which would be the start of the talker's speech lost.
 * The recorder's blocking reads pace the frames. Without the microphone while in use (decision 13) the system gives
 * silence. A read that fails (the record dead, or invalid) builds it again, rather than leave the call unheard.
 */
class Capture(private val send: (ByteArray) -> Unit, failed: (String) -> Unit, private val onReady: () -> Unit) : SpeechIn {
    private val worker = Worker("capture", failed, ::run)

    @Volatile
    override var ready = false
        private set

    @Volatile
    private var mode = 0

    @Volatile
    private var sending = false

    /** Whether the frames recorded go to the session: while the client transmits. */
    override fun send(on: Boolean) {
        sending = on
    }

    /** Starts capturing, in [mode]; another mode of a running capture is its next frame's. */
    override fun start(mode: Int) {
        this.mode = mode.coerceIn(AmrWb.BIT_RATES.indices)
        worker.start()
    }

    /** The mode the server asks for (RFC 4867 §4.3.1), of the frames from the next. */
    override fun mode(mode: Int) {
        this.mode = mode.coerceIn(AmrWb.BIT_RATES.indices)
    }

    override fun stop() = worker.stop()

    // RECORD_AUDIO is checked before the capture starts: Calls.media.
    @SuppressLint("MissingPermission")
    private fun run(running: () -> Boolean) {
        val record = AudioRecord.Builder()
            .setAudioSource(MediaRecorder.AudioSource.VOICE_COMMUNICATION)
            .setAudioFormat(AudioFormat.Builder(pcm).setChannelMask(AudioFormat.CHANNEL_IN_MONO).build())
            .setBufferSizeInBytes(
                maxOf(
                    AudioRecord.getMinBufferSize(AmrWb.SAMPLE_RATE, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT),
                    4 * PCM_BYTES,
                ),
            )
            .build()
        val pcmIn = ByteArray(PCM_BYTES)
        val out = ByteArray(PCM_BYTES)
        var encoder: Codec? = null
        var encoderMode = -1
        val latency = Latency("capture")
        var read = 0L
        try {
            ready = false
            record.startRecording()
            while (running()) {
                if (encoderMode != mode) {
                    encoder?.close()
                    encoderMode = mode
                    encoder = Codec(encoder = true, bitRate = AmrWb.BIT_RATES[encoderMode])
                }
                read(record, pcmIn)
                // The recorder some 400 ms to start: the talk permit told once it records (Calls.granted).
                if (!ready) {
                    ready = true
                    onReady()
                }
                read += AmrWb.FRAME_SAMPLES
                if (read / AmrWb.FRAME_SAMPLES % LATENCY_EVERY == 0L) latency.capture(record, read)
                // Each frame encoded, so that the encoder, its first outputs some hundreds of ms late, is warm at the talk
                // permit; not transmitting, dropped.
                requireNotNull(encoder).process(pcmIn, pcmIn.size, 0) { b ->
                    val n = minOf(b.remaining(), out.size)
                    b.get(out, 0, n)
                    if (sending) AmrWb.split(out, n)?.forEach(send)
                }
            }
        } finally {
            ready = false
            latency.log()
            encoder?.close()
            runCatching { record.stop() }
            record.release()
        }
    }

    private fun read(record: AudioRecord, into: ByteArray) {
        var off = 0
        while (off < into.size) {
            val n = record.read(into, off, into.size - off, AudioRecord.READ_BLOCKING)
            if (n <= 0) throw AudioError("AudioRecord.read: $n")
            off += n
        }
    }
}

private const val JOIN_MS = 500L

/** The frames between two measures of the latency: 2 s. */
private const val LATENCY_EVERY = 100L

/**
 * The latency of the audio path of a call, measured from the timestamps of the platform's audio (AudioTrack and
 * AudioRecord getTimestamp): of the playout, the samples written but not yet heard, and the decoder's, the samples it
 * holds; of the capture, the time from a sample's capture to its read. Logged once the path stops, for the
 * mouth-to-ear latency (TS 22.179 §6.15.3, KPI 3).
 */
private class Latency(private val name: String) {
    private var sum = 0.0
    private var codec = 0.0
    private var n = 0

    fun playout(track: AudioTrack, frames: Long, written: Long) {
        val ts = AudioTimestamp()
        if (!track.getTimestamp(ts)) return
        val presented = ts.framePosition + (System.nanoTime() - ts.nanoTime) * AmrWb.SAMPLE_RATE / 1_000_000_000L
        add(ms(written - presented), ms(frames * AmrWb.FRAME_SAMPLES - written))
    }

    fun capture(record: AudioRecord, read: Long) {
        val ts = AudioTimestamp()
        if (record.getTimestamp(ts, AudioTimestamp.TIMEBASE_MONOTONIC) != AudioRecord.SUCCESS) return
        val sampled = ts.nanoTime + (read - ts.framePosition) * 1_000_000_000L / AmrWb.SAMPLE_RATE
        add((System.nanoTime() - sampled) / 1e6, 0.0)
    }

    private fun add(ms: Double, codecMs: Double) {
        sum += ms
        codec += codecMs
        n++
    }

    fun log() {
        if (n == 0) return
        Log.i(TAG, "$name latency: ${"%.0f".format(sum / n)} ms, codec ${"%.0f".format(codec / n)} ms, $n samples")
    }

    private fun ms(samples: Long) = samples * 1000.0 / AmrWb.SAMPLE_RATE
}

/** A worker's failures in a row before it gives up, the first retried after [BACKOFF_MS], each next after twice. */
private const val RETRIES = 3
private const val BACKOFF_MS = 200L

/** A run as long as this was a working audio path: its failure the first again. */
private const val HEALTHY_MS = 10_000L

/**
 * A thread of audio, running [body] until stopped: each start a thread of its own, told by its token when it is no
 * more the current one, and started once the previous has ended (up to [JOIN_MS]), so that two never hold the audio at
 * once. Stopping does not wait: the thread ends after its frame, off the caller's thread. A failure builds the audio
 * again (the body run again) after a backoff; [RETRIES] in a row, [failed] is told and the thread ends, until started
 * again.
 */
private class Worker(private val name: String, private val failed: (String) -> Unit, private val body: (running: () -> Boolean) -> Unit) {
    private var thread: Thread? = null
    private var last: Thread? = null

    @Synchronized
    fun start() {
        if (thread != null) return
        val before = last
        val t = Thread({ loop(before) }, name)
        thread = t
        last = t
        t.start()
    }

    @Synchronized
    fun stop() {
        val t = thread ?: return
        thread = null
        t.interrupt()
    }

    private fun current(t: Thread) = synchronized(this) { thread === t }

    private fun loop(before: Thread?) {
        val me = Thread.currentThread()
        Process.setThreadPriority(Process.THREAD_PRIORITY_URGENT_AUDIO)
        var failures = 0
        try {
            before?.join(JOIN_MS)
            while (current(me)) {
                val began = SystemClock.elapsedRealtime()
                try {
                    body { current(me) }
                } catch (e: Exception) {
                    if (!current(me)) return
                    if (SystemClock.elapsedRealtime() - began >= HEALTHY_MS) failures = 0
                    failures++
                    Log.e(TAG, "$name failed ($failures of $RETRIES): $e")
                    if (failures >= RETRIES) {
                        failed(e.message ?: e.toString())
                        return
                    }
                    Thread.sleep(BACKOFF_MS shl (failures - 1))
                }
            }
        } catch (_: InterruptedException) {
            // Stopped while waiting.
        } finally {
            synchronized(this) { if (thread === me) thread = null }
        }
    }
}
