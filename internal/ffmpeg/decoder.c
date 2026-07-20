#include "ffmpeg_internal.h"

static void sa_decoder_close_internal(SA_Decoder* decoder) {
	if (decoder == NULL) {
		return;
	}

	if (decoder->resampler != NULL) {
		sa_ffmpeg.swr_free(&decoder->resampler);
	}
	if (decoder->filter_graph != NULL) {
		sa_filter_graph_free(decoder->filter_graph);
	}
	if (decoder->filter_description != NULL) {
		free(decoder->filter_description);
	}
	if (decoder->frame != NULL) {
		sa_ffmpeg.av_frame_free(&decoder->frame);
	}
	if (decoder->packet != NULL) {
		sa_ffmpeg.av_packet_free(&decoder->packet);
	}
	if (decoder->codec_context != NULL) {
		sa_ffmpeg.avcodec_free_context(&decoder->codec_context);
	}
	if (decoder->format_context != NULL) {
		sa_ffmpeg.avformat_close_input(&decoder->format_context);
	}

	free(decoder);
}

static int sa_decoder_create_resampler(SA_Decoder* decoder, int sample_format, char* error, int error_size) {
	if (decoder->codec_parameters->ch_layout.nb_channels <= 0) {
		sa_set_error(error, error_size, "Audio stream has no channel layout");
		return 0;
	}

	SA_AVChannelLayout out_layout;
	SA_AVChannelLayout in_layout = decoder->codec_parameters->ch_layout;
	sa_ffmpeg.av_channel_layout_default(&out_layout, decoder->output_channels);

	SA_SwrContext* resampler = NULL;
	int result = sa_ffmpeg.swr_alloc_set_opts2(
		&resampler,
		&out_layout,
		SA_AV_SAMPLE_FMT_S16,
		decoder->output_sample_rate,
		&in_layout,
		sample_format,
		decoder->codec_parameters->sample_rate,
		0,
		NULL
	);

	if (result < 0) {
		sa_set_error(error, error_size, sa_ffmpeg_error(result));
		return 0;
	}

	result = sa_ffmpeg.swr_init(resampler);
	if (result < 0) {
		sa_ffmpeg.swr_free(&resampler);
		sa_set_error(error, error_size, sa_ffmpeg_error(result));
		return 0;
	}

	decoder->resampler = resampler;
	return 1;
}

int sa_decoder_open(const char* path, const char* filters, int output_channels, SA_Decoder** out_decoder, char* error, int error_size) {
	if (out_decoder == NULL) {
		sa_set_error(error, error_size, "Decoder output pointer is null");
		return 0;
	}
	if (output_channels != 1 && output_channels != 2) {
		sa_set_error(error, error_size, "Decoder output channel count must be 1 or 2");
		return 0;
	}

	*out_decoder = NULL;

	if (!sa_ffmpeg_initialize(error, error_size)) {
		return 0;
	}

	SA_Decoder* decoder = (SA_Decoder*)calloc(1, sizeof(SA_Decoder));
	if (decoder == NULL) {
		sa_set_error(error, error_size, "Failed to allocate decoder state");
		return 0;
	}
	decoder->output_channels = output_channels;

	if (filters != NULL && filters[0] != '\0') {
		decoder->filter_description = sa_duplicate_string(filters);
		if (decoder->filter_description == NULL) {
			sa_set_error(error, error_size, "Failed to allocate audio filter description");
			free(decoder);
			return 0;
		}
	}

	SA_AVFormatContext* format_context = NULL;
	int result = sa_ffmpeg.avformat_open_input(&format_context, path, NULL, NULL);
	if (result < 0) {
		sa_set_error(error, error_size, sa_ffmpeg_error(result));
		sa_decoder_close_internal(decoder);
		return 0;
	}
	decoder->format_context = format_context;

	result = sa_ffmpeg.avformat_find_stream_info(format_context, NULL);
	if (result < 0) {
		sa_set_error(error, error_size, sa_ffmpeg_error(result));
		sa_decoder_close_internal(decoder);
		return 0;
	}

	const SA_AVCodec* codec = NULL;
	int stream_index = sa_ffmpeg.av_find_best_stream(format_context, SA_AVMEDIA_TYPE_AUDIO, -1, -1, &codec, 0);
	if (stream_index < 0) {
		sa_set_error(error, error_size, sa_ffmpeg_error(stream_index));
		sa_decoder_close_internal(decoder);
		return 0;
	}

	if (format_context->streams == NULL || format_context->streams[stream_index] == NULL || format_context->streams[stream_index]->codecpar == NULL) {
		sa_set_error(error, error_size, "Audio stream has no codec parameters");
		sa_decoder_close_internal(decoder);
		return 0;
	}

	decoder->codec_parameters = format_context->streams[stream_index]->codecpar;
	if (decoder->codec_parameters->sample_rate <= 0) {
		sa_set_error(error, error_size, "Audio stream has no sample rate");
		sa_decoder_close_internal(decoder);
		return 0;
	}
	decoder->output_sample_rate = decoder->codec_parameters->sample_rate;

	SA_AVCodecContext* codec_context = sa_ffmpeg.avcodec_alloc_context3(codec);
	if (codec_context == NULL) {
		sa_set_error(error, error_size, "Failed to allocate FFmpeg codec context");
		sa_decoder_close_internal(decoder);
		return 0;
	}
	decoder->codec_context = codec_context;

	result = sa_ffmpeg.avcodec_parameters_to_context(codec_context, decoder->codec_parameters);
	if (result < 0) {
		sa_set_error(error, error_size, sa_ffmpeg_error(result));
		sa_decoder_close_internal(decoder);
		return 0;
	}

	result = sa_ffmpeg.avcodec_open2(codec_context, codec, NULL);
	if (result < 0) {
		sa_set_error(error, error_size, sa_ffmpeg_error(result));
		sa_decoder_close_internal(decoder);
		return 0;
	}

	decoder->packet = sa_ffmpeg.av_packet_alloc();
	decoder->frame = sa_ffmpeg.av_frame_alloc();
	if (decoder->packet == NULL || decoder->frame == NULL) {
		sa_set_error(error, error_size, "Failed to allocate FFmpeg decode buffers");
		sa_decoder_close_internal(decoder);
		return 0;
	}

	decoder->stream_index = stream_index;
	*out_decoder = decoder;
	return 1;
}

static int sa_decoder_filter_frame(SA_Decoder* decoder, int16_t* out, int max_frames, int* written, char* error, int error_size) {
	if (decoder->filter_graph == NULL && !sa_filter_graph_create(decoder, decoder->frame->format, error, error_size)) {
		return 0;
	}

	int result = sa_ffmpeg.av_buffersrc_add_frame_flags(decoder->filter_graph->buffer_source, decoder->frame, SA_AV_BUFFERSRC_FLAG_KEEP_REF);
	if (result < 0) {
		sa_set_error(error, error_size, sa_ffmpeg_error(result));
		return 0;
	}

	return sa_filter_drain(decoder->filter_graph, out, max_frames, written, error, error_size);
}

static int sa_decoder_convert_frame(SA_Decoder* decoder, int16_t* out, int max_frames, int* written, char* error, int error_size) {
	if (decoder->frame->nb_samples <= 0) {
		return 1;
	}

	if (decoder->filter_description != NULL) {
		return sa_decoder_filter_frame(decoder, out, max_frames, written, error, error_size);
	}

	if (decoder->resampler == NULL && !sa_decoder_create_resampler(decoder, decoder->frame->format, error, error_size)) {
		return 0;
	}

	int remaining = max_frames - *written;
	if (remaining <= 0) {
		return 1;
	}

	uint8_t* out_data[1];
	out_data[0] = (uint8_t*)(out + (*written * decoder->output_channels));

	int converted = sa_ffmpeg.swr_convert(
		decoder->resampler,
		out_data,
		remaining,
		(const uint8_t**)decoder->frame->extended_data,
		decoder->frame->nb_samples
	);

	if (converted < 0) {
		sa_set_error(error, error_size, sa_ffmpeg_error(converted));
		return 0;
	}

	*written += converted;
	return 1;
}

static int sa_decoder_flush_resampler(SA_Decoder* decoder, int16_t* out, int max_frames, int* written, int* finished, char* error, int error_size) {
	if (decoder->resampler == NULL) {
		*finished = 1;
		return 1;
	}

	while (*written < max_frames) {
		uint8_t* out_data[1];
		out_data[0] = (uint8_t*)(out + (*written * decoder->output_channels));

		int converted = sa_ffmpeg.swr_convert(decoder->resampler, out_data, max_frames - *written, NULL, 0);
		if (converted < 0) {
			sa_set_error(error, error_size, sa_ffmpeg_error(converted));
			return 0;
		}
		if (converted == 0) {
			*finished = 1;
			return 1;
		}

		*written += converted;
	}

	return 1;
}

int sa_decoder_read(SA_Decoder* decoder, int16_t* out, int max_frames, int* frames_written, int* finished, char* error, int error_size) {
	if (decoder == NULL || out == NULL || frames_written == NULL || finished == NULL || max_frames <= 0) {
		sa_set_error(error, error_size, "Invalid decoder read arguments");
		return 0;
	}

	*frames_written = 0;
	*finished = 0;

	if (decoder->finished) {
		*finished = 1;
		return 1;
	}

	if (decoder->filter_graph != NULL && !decoder->filter_graph->finished) {
		if (!sa_filter_drain(decoder->filter_graph, out, max_frames, frames_written, error, error_size)) {
			return 0;
		}
		if (*frames_written >= max_frames) {
			return 1;
		}
	}

	while (*frames_written < max_frames) {
		int result = sa_ffmpeg.avcodec_receive_frame(decoder->codec_context, decoder->frame);

		if (result == 0) {
			int ok = sa_decoder_convert_frame(decoder, out, max_frames, frames_written, error, error_size);
			sa_ffmpeg.av_frame_unref(decoder->frame);

			if (!ok) {
				return 0;
			}

			continue;
		}

		if (result == SA_AVERROR_EOF) {
			if (decoder->filter_description != NULL) {
				if (decoder->filter_graph == NULL) {
					*finished = 1;
					decoder->finished = 1;
					return 1;
				}

				if (!sa_filter_flush(decoder->filter_graph, out, max_frames, frames_written, error, error_size)) {
					return 0;
				}

				if (decoder->filter_graph->finished) {
					*finished = 1;
					decoder->finished = 1;
				}

				return 1;
			}

			if (!sa_decoder_flush_resampler(decoder, out, max_frames, frames_written, finished, error, error_size)) {
				return 0;
			}

			if (*finished) {
				decoder->finished = 1;
			}

			return 1;
		}

		if (result != SA_AVERROR_EAGAIN) {
			sa_set_error(error, error_size, sa_ffmpeg_error(result));
			return 0;
		}

		if (decoder->sent_flush) {
			continue;
		}

		while (1) {
			result = sa_ffmpeg.av_read_frame(decoder->format_context, decoder->packet);

			if (result == SA_AVERROR_EOF) {
				decoder->sent_flush = 1;
				result = sa_ffmpeg.avcodec_send_packet(decoder->codec_context, NULL);

				if (result < 0 && result != SA_AVERROR_EOF) {
					sa_set_error(error, error_size, sa_ffmpeg_error(result));
					return 0;
				}

				break;
			}

			if (result < 0) {
				sa_set_error(error, error_size, sa_ffmpeg_error(result));
				return 0;
			}

			if (decoder->packet->stream_index != decoder->stream_index) {
				sa_ffmpeg.av_packet_unref(decoder->packet);
				continue;
			}

			while (1) {
				result = sa_ffmpeg.avcodec_send_packet(decoder->codec_context, decoder->packet);

				if (result == SA_AVERROR_EAGAIN) {
					break;
				}
				if (result < 0 && result != SA_AVERROR_EOF) {
					sa_ffmpeg.av_packet_unref(decoder->packet);
					sa_set_error(error, error_size, sa_ffmpeg_error(result));
					return 0;
				}

				break;
			}

			sa_ffmpeg.av_packet_unref(decoder->packet);
			break;
		}
	}

	return 1;
}

void sa_decoder_close(SA_Decoder* decoder) {
	sa_decoder_close_internal(decoder);
}

int sa_decoder_sample_rate(SA_Decoder* decoder) {
	if (decoder == NULL) {
		return 0;
	}

	return decoder->output_sample_rate;
}

int sa_decoder_channels(SA_Decoder* decoder) {
	if (decoder == NULL || decoder->codec_parameters == NULL) {
		return 0;
	}

	return decoder->codec_parameters->ch_layout.nb_channels;
}

int64_t sa_decoder_bit_rate(SA_Decoder* decoder) {
	if (decoder == NULL || decoder->codec_parameters == NULL) {
		return 0;
	}

	return decoder->codec_parameters->bit_rate;
}

double sa_decoder_duration(SA_Decoder* decoder) {
	if (decoder == NULL || decoder->format_context == NULL || decoder->format_context->streams == NULL || decoder->stream_index < 0) {
		return -1;
	}

	SA_AVStream* stream = decoder->format_context->streams[decoder->stream_index];
	if (stream == NULL || stream->duration == SA_AV_NOPTS_VALUE || stream->duration <= 0 || stream->time_base.num <= 0 || stream->time_base.den <= 0) {
		return -1;
	}

	return (double)stream->duration * (double)stream->time_base.num / (double)stream->time_base.den;
}

int sa_decoder_tag(SA_Decoder* decoder, int index, const char** key, const char** value) {
	if (key != NULL) {
		*key = NULL;
	}
	if (value != NULL) {
		*value = NULL;
	}
	if (decoder == NULL || decoder->format_context == NULL || decoder->format_context->metadata == NULL || index < 0 || key == NULL || value == NULL) {
		return 0;
	}

	const SA_AVDictionaryEntry* entry = NULL;

	for (int current_index = 0; current_index <= index; current_index++) {
		entry = sa_ffmpeg.av_dict_iterate(decoder->format_context->metadata, entry);
		if (entry == NULL) {
			return 0;
		}
	}

	*key = entry->key;
	*value = entry->value;

	return 1;
}
