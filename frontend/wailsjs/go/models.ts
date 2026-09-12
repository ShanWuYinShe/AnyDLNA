export namespace main {
	
	export class BrowserInfo {
	    name: string;
	    path: string;
	
	    static createFrom(source: any = {}) {
	        return new BrowserInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	    }
	}
	export class CastStatus {
	    active: boolean;
	    device: string;
	    file: string;
	    mode: string;
	
	    static createFrom(source: any = {}) {
	        return new CastStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.active = source["active"];
	        this.device = source["device"];
	        this.file = source["file"];
	        this.mode = source["mode"];
	    }
	}
	export class DeviceInfo {
	    udn: string;
	    name: string;
	    model: string;
	    host: string;
	
	    static createFrom(source: any = {}) {
	        return new DeviceInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.udn = source["udn"];
	        this.name = source["name"];
	        this.model = source["model"];
	        this.host = source["host"];
	    }
	}
	export class LoginBrowserInfo {
	    supported: boolean;
	    running: boolean;
	    executable: string;
	    browsers: BrowserInfo[];
	
	    static createFrom(source: any = {}) {
	        return new LoginBrowserInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.supported = source["supported"];
	        this.running = source["running"];
	        this.executable = source["executable"];
	        this.browsers = this.convertValues(source["browsers"], BrowserInfo);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class PickedVideo {
	    path: string;
	    name: string;
	    durationSec: number;
	    videoCodec: string;
	    audioCodec: string;
	    width: number;
	    height: number;
	    sizeMB: number;
	    directPlay: boolean;
	
	    static createFrom(source: any = {}) {
	        return new PickedVideo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.name = source["name"];
	        this.durationSec = source["durationSec"];
	        this.videoCodec = source["videoCodec"];
	        this.audioCodec = source["audioCodec"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.sizeMB = source["sizeMB"];
	        this.directPlay = source["directPlay"];
	    }
	}
	export class Position {
	    positionSec: number;
	    durationSec: number;
	    state: string;
	
	    static createFrom(source: any = {}) {
	        return new Position(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.positionSec = source["positionSec"];
	        this.durationSec = source["durationSec"];
	        this.state = source["state"];
	    }
	}
	export class ResolvedInfo {
	    url: string;
	    title: string;
	    durationSec: number;
	    isLive: boolean;
	    extractor: string;
	    uploader: string;
	
	    static createFrom(source: any = {}) {
	        return new ResolvedInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.url = source["url"];
	        this.title = source["title"];
	        this.durationSec = source["durationSec"];
	        this.isLive = source["isLive"];
	        this.extractor = source["extractor"];
	        this.uploader = source["uploader"];
	    }
	}

}

export namespace media {
	
	export class Config {
	    proxyMode: string;
	    proxyUrl: string;
	    cookieMode: string;
	    cookieBrowser: string;
	
	    static createFrom(source: any = {}) {
	        return new Config(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.proxyMode = source["proxyMode"];
	        this.proxyUrl = source["proxyUrl"];
	        this.cookieMode = source["cookieMode"];
	        this.cookieBrowser = source["cookieBrowser"];
	    }
	}
	export class CookiesInfo {
	    exists: boolean;
	    count: number;
	    savedAt: string;
	    path: string;
	
	    static createFrom(source: any = {}) {
	        return new CookiesInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.exists = source["exists"];
	        this.count = source["count"];
	        this.savedAt = source["savedAt"];
	        this.path = source["path"];
	    }
	}

}

