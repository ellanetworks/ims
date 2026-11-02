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

// RFC 4867 section 5.3
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

interface SpeechOut {
    fun start()

    fun stop()
}

interface SpeechIn {
    val ready: Boolean

    fun start(mode: Int)

    fun stop()

    fun send(on: Boolean)

    fun mode(mode: Int)
}

interface Audio {
    fun playout(next: () -> ByteArray, failed: (String) -> Unit): SpeechOut

    fun capture(send: (ByteArray) -> Unit, failed: (String) -> Unit, ready: () -> Unit): SpeechIn
}

object DeviceAudio : Audio {
    override fun playout(next: () -> ByteArray, failed: (String) -> Unit): SpeechOut = Playout(next, failed)

    override fun capture(send: (ByteArray) -> Unit, failed: (String) -> Unit, ready: () -> Unit): SpeechIn = Capture(send, failed, ready)
}

private class AudioError(message: String) : Exception(message)

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

class Capture(private val send: (ByteArray) -> Unit, failed: (String) -> Unit, private val onReady: () -> Unit) : SpeechIn {
    private val worker = Worker("capture", failed, ::run)

    @Volatile
    override var ready = false
        private set

    @Volatile
    private var mode = 0

    @Volatile
    private var sending = false

    override fun send(on: Boolean) {
        sending = on
    }

    override fun start(mode: Int) {
        this.mode = mode.coerceIn(AmrWb.BIT_RATES.indices)
        worker.start()
    }

    // RFC 4867 section 4.3.1
    override fun mode(mode: Int) {
        this.mode = mode.coerceIn(AmrWb.BIT_RATES.indices)
    }

    override fun stop() = worker.stop()

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
                if (!ready) {
                    ready = true
                    onReady()
                }
                read += AmrWb.FRAME_SAMPLES
                if (read / AmrWb.FRAME_SAMPLES % LATENCY_EVERY == 0L) latency.capture(record, read)
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

private const val LATENCY_EVERY = 100L

// TS 22.179 clause 6.15.3
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

private const val RETRIES = 3
private const val BACKOFF_MS = 200L

private const val HEALTHY_MS = 10_000L

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
        } finally {
            synchronized(this) { if (thread === me) thread = null }
        }
    }
}
