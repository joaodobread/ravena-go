package translate

import (
	"slices"
	"strings"
)

// languages são os idiomas aceitos (códigos do Google) com o nome em português.
var languages = map[string]string{
	"af": "Africâner", "sq": "Albanês", "am": "Amárico", "ar": "Árabe", "hy": "Armênio",
	"az": "Azerbaijano", "eu": "Basco", "be": "Bielorrusso", "bn": "Bengali", "bs": "Bósnio",
	"bg": "Búlgaro", "ca": "Catalão", "ceb": "Cebuano", "ny": "Chichewa",
	"zh-cn": "Chinês (Simplificado)", "zh-tw": "Chinês (Tradicional)", "co": "Corso",
	"hr": "Croata", "cs": "Tcheco", "da": "Dinamarquês", "nl": "Holandês", "en": "Inglês",
	"eo": "Esperanto", "et": "Estoniano", "tl": "Filipino", "fi": "Finlandês", "fr": "Francês",
	"fy": "Frísio", "gl": "Galego", "ka": "Georgiano", "de": "Alemão", "el": "Grego",
	"gu": "Guzerate", "ht": "Crioulo Haitiano", "ha": "Hauçá", "haw": "Havaiano", "iw": "Hebraico",
	"hi": "Hindi", "hmn": "Hmong", "hu": "Húngaro", "is": "Islandês", "ig": "Igbo",
	"id": "Indonésio", "ga": "Irlandês", "it": "Italiano", "ja": "Japonês", "jw": "Javanês",
	"kn": "Canarês", "kk": "Cazaque", "km": "Khmer", "ko": "Coreano", "ku": "Curdo",
	"ky": "Quirguiz", "lo": "Laosiano", "la": "Latim", "lv": "Letão", "lt": "Lituano",
	"lb": "Luxemburguês", "mk": "Macedônio", "mg": "Malgaxe", "ms": "Malaio", "ml": "Malaiala",
	"mt": "Maltês", "mi": "Maori", "mr": "Marati", "mn": "Mongol", "my": "Birmanês",
	"ne": "Nepalês", "no": "Norueguês", "ps": "Pashto", "fa": "Persa", "pl": "Polonês",
	"pt": "Português", "pa": "Punjabi", "ro": "Romeno", "ru": "Russo", "sm": "Samoano",
	"gd": "Gaélico Escocês", "sr": "Sérvio", "st": "Sesoto", "sn": "Shona", "sd": "Sindi",
	"si": "Cingalês", "sk": "Eslovaco", "sl": "Esloveno", "so": "Somali", "es": "Espanhol",
	"su": "Sundanês", "sw": "Suaíli", "sv": "Sueco", "tg": "Tadjique", "ta": "Tâmil",
	"te": "Telugo", "th": "Tailandês", "tr": "Turco", "uk": "Ucraniano", "ur": "Urdu",
	"uz": "Uzbeque", "vi": "Vietnamita", "cy": "Galês", "xh": "Xhosa", "yi": "Iídiche",
	"yo": "Iorubá", "zu": "Zulu",
}

// aliases mapeia nomes digitados para códigos.
var aliases = map[string]string{
	"inglês": "en", "ingles": "en", "english": "en",
	"português": "pt", "portugues": "pt", "portuguese": "pt", "brasileiro": "pt",
	"brasil": "pt", "brazil": "pt", "pt-br": "pt", "pt-pt": "pt",
	"espanhol": "es", "spanish": "es", "español": "es",
	"francês": "fr", "frances": "fr", "french": "fr",
	"alemão": "de", "alemao": "de", "german": "de",
	"italiano": "it", "italian": "it",
	"japonês": "ja", "japones": "ja", "japanese": "ja",
	"chinês": "zh-cn", "chines": "zh-cn", "chinese": "zh-cn", "zh": "zh-cn",
	"russo": "ru", "russian": "ru",
	"árabe": "ar", "arabe": "ar", "arabic": "ar",
	"coreano": "ko", "korean": "ko",
	"he": "iw", "hebraico": "iw",
}

// flagLanguage é o mapa de bandeiras do Ravena (TranslationCommands.js).
var flagLanguage = map[string]string{
	"🇦🇷": "es", "🇦🇹": "de", "🇦🇺": "en", "🇧🇪": "fr", "🇧🇷": "pt", "🇨🇦": "en",
	"🇨🇭": "de", "🇨🇱": "es", "🇨🇳": "zh-cn", "🇨🇴": "es", "🇨🇿": "cs", "🇩🇪": "de",
	"🇩🇰": "da", "🇪🇦": "es", "🇪🇬": "ar", "🇪🇸": "es", "🇫🇮": "fi", "🇫🇷": "fr",
	"🇬🇧": "en", "🇬🇷": "el", "🇭🇰": "zh-tw", "🇭🇺": "hu", "🇮🇩": "id", "🇮🇪": "en",
	"🇮🇱": "iw", "🇮🇳": "hi", "🇮🇷": "fa", "🇮🇸": "is", "🇮🇹": "it", "🇯🇵": "ja",
	"🇰🇷": "ko", "🇲🇽": "es", "🇲🇾": "ms", "🇳🇱": "nl", "🇳🇴": "no", "🇳🇿": "en",
	"🇵🇪": "es", "🇵🇭": "tl", "🇵🇱": "pl", "🇵🇹": "pt", "🇷🇴": "ro", "🇷🇺": "ru",
	"🇸🇦": "ar", "🇸🇪": "sv", "🇸🇬": "en", "🇹🇭": "th", "🇹🇷": "tr", "🇹🇼": "zh-tw",
	"🇺🇦": "uk", "🇺🇸": "en", "🇻🇳": "vi", "🇿🇦": "en",
}

func flagTriggers() []string {
	out := make([]string, 0, len(flagLanguage))
	for f := range flagLanguage {
		out = append(out, f)
	}
	slices.Sort(out)
	return out
}

// languageCode resolve um código ("en"), apelido ("inglês") ou nome ("Inglês").
func languageCode(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if _, ok := languages[s]; ok {
		return s, true
	}
	if c, ok := aliases[s]; ok {
		return c, true
	}
	for code, name := range languages {
		if strings.ToLower(name) == s {
			return code, true
		}
	}
	return "", false
}

func languageName(code string) string {
	code = strings.ToLower(code)
	if n, ok := languages[code]; ok {
		return n
	}
	if c, ok := aliases[code]; ok {
		return languages[c]
	}
	return code
}

// apiCode converte para a grafia do Google ("zh-cn" → "zh-CN").
func apiCode(code string) string {
	if a, b, ok := strings.Cut(code, "-"); ok {
		return a + "-" + strings.ToUpper(b)
	}
	return code
}

// safeTargets: muitos códigos também são palavras comuns ("eu", "de",
// "it", "es"...). Na forma "origem destino texto" só aceitamos como destino
// os códigos que não se confundem com o começo do texto (o nome por extenso,
// "alemão", sempre funciona). Ex.: "!traduzir en eu te amo" traduz
// "eu te amo" para o inglês.
var safeTargets = map[string]bool{
	"pt": true, "en": true, "fr": true, "ja": true, "ko": true, "ru": true, "zh": true,
	"zh-cn": true, "zh-tw": true, "ar": true, "pl": true, "nl": true, "sv": true, "tr": true,
	"uk": true, "iw": true, "pt-br": true,
}

// targetCode resolve o segundo argumento da forma "origem destino texto".
func targetCode(s string) (string, bool) {
	l := strings.ToLower(strings.TrimSpace(s))
	if _, isCode := languages[l]; isCode && !safeTargets[l] {
		return "", false
	}
	return languageCode(l)
}
