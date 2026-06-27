#ifndef SIMPLE_AUDIO_FFMPEG_H
#define SIMPLE_AUDIO_FFMPEG_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct SA_Decoder SA_Decoder;

int sa_ffmpeg_initialize(char* error, int error_size);
int sa_decoder_open(const char* path, const char* filters, int output_channels, SA_Decoder** out_decoder, char* error, int error_size);
int sa_decoder_read(SA_Decoder* decoder, int16_t* out, int max_frames, int* frames_written, int* finished, char* error, int error_size);
void sa_decoder_close(SA_Decoder* decoder);
int sa_decoder_sample_rate(SA_Decoder* decoder);
int sa_decoder_channels(SA_Decoder* decoder);
int64_t sa_decoder_bit_rate(SA_Decoder* decoder);
double sa_decoder_duration(SA_Decoder* decoder);

#ifdef __cplusplus
}
#endif

#endif