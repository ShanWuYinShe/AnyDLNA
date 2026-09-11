export namespace main {
	
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

}

